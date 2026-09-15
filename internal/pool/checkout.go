package pool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/asalimonov/montygo/internal/value"
	"github.com/asalimonov/montygo/internal/wire"
	"github.com/asalimonov/montygo/internal/worker"
)

// MountTable services OS calls from host-side mounts for one feed.
type MountTable interface {
	HandleOsCall(ctx context.Context, call *wire.OsCall) (handled bool, result any, exc *wire.Exception)
}

// OnPrint receives sandbox output (stream 1 stdout, 2 stderr).
type OnPrint func(stream uint8, text string)

// CheckoutOptions carries per-checkout transport context.
type CheckoutOptions struct {
	// Observe creates the checkout's observer once a worker is acquired, before
	// Configure is sent; hasPID is false for a worker without a process.
	Observe func(pid int, hasPID bool) Observer
}

type pendingKind uint8

const (
	pendingNone pendingKind = iota
	pendingCall
	pendingLookup
	pendingFutures
)

type pending struct {
	kind       pendingKind
	callID     uint32
	name       string
	os         *wire.OsCall
	allowEager bool
}

// Checkout is one worker dedicated to one REPL session.
type Checkout struct {
	pool        *Pool
	mu          sync.Mutex
	lease       checkoutLease
	pending     pending
	feedMounts  MountTable
	budget      sessionBudget
	inFlight    bool
	cancelled   bool
	cwdSet      bool
	abortFlight bool
	sent        bool
	started     time.Time
	obs         *observer
	worker      worker.Worker
	done        <-chan struct{}
	metricsOnce sync.Once
}

// Checkout acquires a worker and configures its session.
func (p *Pool) Checkout(ctx context.Context, cfg wire.Configure, opts CheckoutOptions) (*Checkout, error) {
	s, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	cfg.MontyVersion = p.cfg.MontyVersion
	cfg.ProtocolVersion = p.cfg.ProtocolVersion
	c := p.newCheckout(s, opts)
	c.applyLimits(cfg)
	ev, err := c.turn(ctx, cfg, true, nil)
	if err != nil {
		c.drop("discarded")
		return nil, err
	}
	if ev.Kind != wire.EventOk {
		c.discard("discarded")
		return nil, protocolError("unexpected reply to Configure: %s", ev.Kind)
	}
	return c, nil
}

func (p *Pool) newCheckout(s *slot, opts CheckoutOptions) *Checkout {
	c := &Checkout{pool: p, lease: checkoutLease{slot: s, worker: s.w}, started: time.Now(), worker: s.w, done: s.w.Done()}
	if opts.Observe != nil {
		pid, hasPID := s.w.PID()
		if o := opts.Observe(pid, hasPID); o != nil {
			s.obs = &observer{o: o}
			c.obs = s.obs
		}
	}
	return c
}

func (c *Checkout) applyLimits(cfg wire.Configure) {
	c.budget.suspensionLimit = DefaultMaxSuspensions
	if cfg.Limits == nil {
		return
	}
	if cfg.Limits.MaxDurationMicros != nil {
		d := time.Duration(*cfg.Limits.MaxDurationMicros) * time.Microsecond
		c.budget.durationBudget = &d
	}
	if cfg.Limits.MaxSuspensions != nil {
		c.budget.suspensionLimit = *cfg.Limits.MaxSuspensions
	}
}

// PID returns the worker's process id when it has one and no turn is running.
func (c *Checkout) PID() (int, bool) {
	if !c.mu.TryLock() {
		return 0, false
	}
	defer c.mu.Unlock()
	if !c.lease.active() || c.inFlight {
		return 0, false
	}
	return c.worker.PID()
}

// Finished reports whether the worker is gone.
func (c *Checkout) Finished() bool {
	return !c.lease.active()
}

// Kind returns the transport kind.
func (c *Checkout) Kind() worker.Kind { return c.pool.cfg.Spawner.Kind() }

func (c *Checkout) ensureReady() error {
	if c.cancelled {
		c.cancelled = false
		return &Error{Kind: KindCancelled}
	}
	if !c.lease.active() {
		return &Error{Kind: KindFinished}
	}
	if c.inFlight {
		c.discard("discarded")
		return &Error{Kind: KindCancelled}
	}
	return nil
}

func (c *Checkout) drop(reason string) {
	c.discard(reason)
}

func (c *Checkout) discard(reason string) {
	c.pending = pending{}
	c.feedMounts = nil
	c.inFlight = false
	c.terminate(nil, reason, false)
}

func (c *Checkout) turn(ctx context.Context, req wire.Request, control bool, onPrint OnPrint) (*wire.Event, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	if !c.obs.acquire() {
		return nil, &Error{Kind: KindFinished}
	}
	defer c.obs.release()
	for _, v := range wire.RequestValues(req) {
		if value.ExceedsMaxDepth(v) {
			return nil, runtimeError("RuntimeError", "Max input depth exceeded")
		}
	}
	payload, err := wire.EncodeRequest(req, "")
	if err != nil {
		var conv *wire.ConversionError
		if errors.As(err, &conv) {
			return nil, runtimeError("TypeError", conv.Message)
		}
		return nil, runtimeError("RuntimeError", err.Error())
	}
	if len(payload) > wire.MaxFrameLen {
		return nil, runtimeError("RuntimeError", fmt.Sprintf("request frame of %d bytes exceeds the maximum of %d bytes", len(payload), wire.MaxFrameLen))
	}
	deadline, hasDeadline := c.budget.deadline(c.pool.cfg.RequestTimeout, c.pool.cfg.DurationLimitGrace, c.pool.cfg.GraceDisabled, control)
	tctx := ctx
	if hasDeadline {
		var cancel context.CancelFunc
		tctx, cancel = context.WithTimeout(ctx, deadline)
		defer cancel()
	}
	c.inFlight = true
	c.sent = false
	w := c.worker
	if err := c.send(tctx, w, req, payload); err != nil {
		return nil, c.ioFailure(ctx, tctx, err, "sending a request", deadline)
	}
	c.sent = true
	for {
		raw, err := w.Recv(tctx)
		if err != nil {
			return nil, c.ioFailure(ctx, tctx, err, "waiting for a reply", deadline)
		}
		ev, err := wire.DecodeEvent(raw)
		if err != nil {
			c.discard("discarded")
			return nil, protocolError("invalid payload from worker: %v", err)
		}
		c.obs.received(ev, len(raw))
		if ev.Kind == wire.EventPrint {
			if onPrint != nil {
				for _, seg := range ev.Print {
					onPrint(seg.Stream, seg.Text)
				}
			}
			continue
		}
		c.budget.observe(ev)
		if c.abortFlight {
			c.abortFlight = false
			if ev.Kind != wire.EventError && ev.Kind != wire.EventFatalError && ev.Kind != wire.EventShutdown {
				c.discard("discarded")
				return nil, protocolError("worker answered AbortFeed with something other than an Error")
			}
		} else if ev.IsSuspension() && c.budget.suspensionsSeen > c.budget.suspensionLimit {
			if ev.Kind == wire.EventOsCall && (ev.OsCall.Op == 0 || ev.OsCall.PayloadErr != nil) {
				return c.dispatch(ctx, ev, req)
			}
			abortReq := wire.AbortFeed{Exception: wire.NewException("RuntimeError", fmt.Sprintf("suspension limit %d exceeded", c.budget.suspensionLimit))}
			abort, _ := wire.EncodeRequest(abortReq, "")
			if err := c.send(tctx, w, abortReq, abort); err != nil {
				return nil, c.ioFailure(ctx, tctx, err, "aborting a feed", deadline)
			}
			c.abortFlight = true
			continue
		}
		c.inFlight = false
		return c.dispatch(ctx, ev, req)
	}
}

func (c *Checkout) ioFailure(ctx, tctx context.Context, err error, doing string, deadline time.Duration) error {
	switch {
	case ctx.Err() != nil:
		c.discard("abandoned")
		c.cancelled = true
		return &Error{Kind: KindCancelled, Cause: ctx.Err()}
	case tctx.Err() != nil:
		return c.poisonTimeout(deadline)
	}
	var tooLarge *wire.FrameTooLargeError
	if errors.As(err, &tooLarge) {
		c.discard("discarded")
		return protocolError("invalid payload from worker: %v", err)
	}
	return c.poison(doing)
}

func (c *Checkout) poisonTimeout(deadline time.Duration) error {
	c.discard("turn_timeout")
	return &Error{Kind: KindTimeout, Timeout: deadline, WorkerLost: true}
}

func (c *Checkout) poison(doing string) error {
	if c.worker.Kind() == worker.KindWebSocket {
		c.discard("disconnected")
		perr := &Error{Kind: KindDisconnected, Message: doing, WorkerLost: true}
		var closed *worker.ClosedError
		if errors.As(c.worker.Err(), &closed) {
			perr.CloseCode, perr.CloseReason = closed.Code, closed.Reason
		}
		return perr
	}
	status := reap(c.worker, fatalExitGrace)
	if status.Exited && status.Code == 65 {
		c.discard("oom")
		return &Error{Kind: KindRuntime, Exception: wire.NewException("MemoryError", "the worker exceeded its memory limit and was terminated"), WorkerLost: true}
	}
	c.discard("crash")
	return &Error{Kind: KindCrashed, Message: doing, Status: status, WorkerLost: true}
}

func reap(w worker.Worker, grace time.Duration) worker.Status {
	if w.Kind() == worker.KindWebSocket {
		return worker.Status{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	status, ok := w.Wait(ctx)
	cancel()
	if ok {
		return status
	}
	w.Kill()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, _ = w.Wait(ctx)
	return status
}

func (c *Checkout) dispatch(ctx context.Context, ev *wire.Event, req wire.Request) (*wire.Event, error) {
	switch ev.Kind {
	case wire.EventFunctionCall:
		fc := ev.FunctionCall
		c.pending = pending{kind: pendingCall, callID: fc.CallID, name: fc.FunctionName, allowEager: fc.AllowEagerAwait}
		return ev, nil
	case wire.EventOsCall:
		if ev.OsCall.Op == 0 {
			c.discard("discarded")
			return nil, protocolError("OsCall event with no call")
		}
		if ev.OsCall.PayloadErr != nil {
			c.discard("discarded")
			return nil, protocolError("invalid OS call payload: %v", ev.OsCall.PayloadErr)
		}
		c.pending = pending{kind: pendingCall, callID: ev.OsCall.CallID, name: ev.OsCall.Name(), os: ev.OsCall}
		return ev, nil
	case wire.EventNameLookup:
		c.pending = pending{kind: pendingLookup, name: ev.NameLookup.Name}
		return ev, nil
	case wire.EventResolveFutures:
		c.pending = pending{kind: pendingFutures}
		return ev, nil
	case wire.EventComplete:
		c.pending = pending{}
		c.feedMounts = nil
		if !ev.HasValue {
			c.discard("discarded")
			return nil, protocolError("complete event with no value")
		}
		return ev, nil
	case wire.EventError:
		if _, isDump := req.(wire.Dump); !isDump {
			c.pending = pending{}
			c.feedMounts = nil
		}
		if ev.Exception == nil {
			c.discard("discarded")
			return nil, protocolError("error event with no exception")
		}
		return nil, &Error{Kind: KindRuntime, Exception: ev.Exception}
	case wire.EventTypingError:
		c.pending = pending{}
		c.feedMounts = nil
		return nil, &Error{Kind: KindTyping, Diagnostics: ev.Diagnostics}
	case wire.EventOk, wire.EventDumpResult:
		return ev, nil
	case wire.EventFatalError:
		status := reap(c.worker, fatalExitGrace)
		c.discard("fatal")
		return nil, &Error{Kind: KindCrashed, Announced: true, Message: ev.FatalMessage, Status: status, WorkerLost: true}
	case wire.EventShutdown:
		if c.worker.Kind() != worker.KindWebSocket {
			c.discard("discarded")
			return nil, protocolError("subprocess worker sent a ShutdownDump")
		}
		c.discard("disconnected")
		return nil, &Error{Kind: KindShutdown, Dump: ev.ShutdownDump, HasDump: ev.HasShutdownDump, WorkerLost: true}
	}
	c.discard("discarded")
	return nil, protocolError("unexpected event")
}

// Feed runs one snippet. mounts may be nil; firstMount is the first mount's virtual path.
func (c *Checkout) Feed(ctx context.Context, code string, inputs []wire.NamedValue, mounts MountTable, firstMount string, cwd *string, skipTypeCheck bool, onPrint OnPrint) (*wire.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	if c.pending.kind != pendingNone {
		return nil, protocolError("feed called while a suspension is awaiting an answer")
	}
	dir := ""
	switch {
	case cwd != nil:
		valid, err := value.ValidateCwd(*cwd)
		if err != nil {
			return nil, runtimeError("ValueError", err.Error())
		}
		dir = valid
	case !c.cwdSet:
		dir = "/"
		if firstMount != "" {
			dir = firstMount
		}
	}
	c.feedMounts = mounts
	ev, err := c.turn(ctx, wire.Feed{Code: code, Inputs: inputs, SkipTypeCheck: skipTypeCheck, Cwd: dir}, false, onPrint)
	var perr *Error
	if c.sent && (!errors.As(err, &perr) || perr.Kind != KindTyping) {
		c.cwdSet = true
	}
	return ev, err
}

// Resume answers a pending FunctionCall or OsCall.
func (c *Checkout) Resume(ctx context.Context, result wire.ExtResult, onPrint OnPrint) (*wire.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resumeLocked(ctx, result, onPrint)
}

func (c *Checkout) resumeLocked(ctx context.Context, result wire.ExtResult, onPrint OnPrint) (*wire.Event, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	if c.pending.kind != pendingCall {
		return nil, protocolError("no suspended call to resume")
	}
	if result.Kind == wire.ExtNotHandled && c.pending.os == nil {
		return nil, protocolError("NotHandled is only valid answering an OS call")
	}
	switch result.Kind {
	case wire.ExtFuture:
		result.FutureCallID = c.pending.callID
	case wire.ExtNotFound:
		result.NotFoundName = c.pending.name
	}
	return c.turn(ctx, wire.ResumeCall{CallID: c.pending.callID, Result: result}, false, onPrint)
}

// ResumeNameLookup answers a pending NameLookup.
func (c *Checkout) ResumeNameLookup(ctx context.Context, result wire.ResumeNameLookup, onPrint OnPrint) (*wire.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	if c.pending.kind != pendingLookup {
		return nil, protocolError("no suspended name lookup to resume")
	}
	return c.turn(ctx, result, false, onPrint)
}

// ResumeFutures answers ResolveFutures, or an eager call with its single result.
func (c *Checkout) ResumeFutures(ctx context.Context, results []wire.FutureResult, onPrint OnPrint) (*wire.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	switch {
	case c.pending.kind == pendingFutures:
	case c.pending.kind == pendingCall && c.pending.allowEager:
		if len(results) != 1 || results[0].CallID != c.pending.callID {
			return nil, protocolError("eager result must match the suspended call id")
		}
	default:
		return nil, protocolError("no suspended futures to resume")
	}
	for _, r := range results {
		if r.Result.Kind != wire.ExtReturn && r.Result.Kind != wire.ExtError {
			return nil, protocolError("future %d must resolve to Return or Error", r.CallID)
		}
	}
	return c.turn(ctx, wire.ResumeFutures{Results: results}, false, onPrint)
}

// ResumeFromMounts offers the pending OS call to the feed's mounts.
func (c *Checkout) ResumeFromMounts(ctx context.Context, onPrint OnPrint) (*wire.Event, bool, error) {
	handled, res, err := c.HandlePendingMount(ctx)
	if !handled || err != nil {
		return nil, handled, err
	}
	ev, err := c.Resume(ctx, res, onPrint)
	var perr *Error
	if errors.As(err, &perr) && perr.Kind == KindRuntime && perr.PreSend {
		ev, err = c.Resume(ctx, wire.ExtResult{Kind: wire.ExtError, Error: perr.Exception}, onPrint)
	}
	return ev, true, err
}

// HandlePendingMount services a pending mount call without resuming the worker.
func (c *Checkout) HandlePendingMount(ctx context.Context) (bool, wire.ExtResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureReady(); err != nil {
		return false, wire.ExtResult{}, err
	}
	if c.pending.kind != pendingCall {
		return false, wire.ExtResult{}, protocolError("no suspended call to resume")
	}
	if c.pending.os == nil {
		return false, wire.ExtResult{}, protocolError("resume_from_mounts is only valid answering an OS call")
	}
	if c.feedMounts == nil {
		return false, wire.ExtResult{}, nil
	}
	c.inFlight = true
	handled, result, exc := c.feedMounts.HandleOsCall(ctx, c.pending.os)
	c.inFlight = false
	if !handled {
		return false, wire.ExtResult{}, nil
	}
	res := wire.ExtResult{Kind: wire.ExtReturn, Value: result}
	if exc != nil {
		res = wire.ExtResult{Kind: wire.ExtError, Error: exc}
	}
	return true, res, nil
}

// Dump serializes the session.
func (c *Checkout) Dump(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ev, err := c.turn(ctx, wire.Dump{}, true, nil)
	if err != nil {
		return nil, err
	}
	if ev.Kind != wire.EventDumpResult {
		c.discard("discarded")
		return nil, protocolError("unexpected reply to Dump: %s", ev.Kind)
	}
	return ev.State, nil
}

// Restore loads a dump into this fresh session. A nil event means the dump was idle.
func (c *Checkout) Restore(ctx context.Context, state []byte, mounts MountTable, onPrint OnPrint) (*wire.Event, *string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureReady(); err != nil {
		return nil, nil, err
	}
	c.pending = pending{}
	saved := c.budget
	c.budget.durationBudget = nil
	c.budget.reportedExec = 0
	c.budget.suspensionsSeen = 0
	c.feedMounts = mounts
	ev, err := c.turn(ctx, wire.Load{State: state}, true, onPrint)
	if err != nil {
		var perr *Error
		if !errors.As(err, &perr) || !perr.WorkerLost {
			limit := c.budget.suspensionLimit
			c.budget = saved
			if limit < c.budget.suspensionLimit {
				c.budget.suspensionLimit = limit
			}
		}
		return nil, nil, err
	}
	c.cwdSet = true
	switch {
	case ev.Kind == wire.EventOk:
		return nil, ev.RestoredScriptName, nil
	case ev.IsSuspension():
		return ev, ev.RestoredScriptName, nil
	}
	c.discard("discarded")
	return nil, nil, protocolError("unexpected reply to Load: %s", ev.Kind)
}

// InstallDependencies installs packages into a CPython worker's session.
func (c *Checkout) InstallDependencies(ctx context.Context, requirements []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureReady(); err != nil {
		return err
	}
	if c.pending.kind != pendingNone {
		return protocolError("install_dependencies called while a suspension is awaiting an answer")
	}
	if len(requirements) == 0 {
		return nil
	}
	for _, r := range requirements {
		trimmed := strings.TrimSpace(r)
		switch {
		case trimmed == "":
			return runtimeError("ValueError", fmt.Sprintf("invalid requirement %s: must not be empty", value.RustDebugString(r)))
		case strings.HasPrefix(trimmed, "-"):
			return runtimeError("ValueError", fmt.Sprintf("invalid requirement %s: must not start with '-' (it would be parsed as a uv option)", value.RustDebugString(r)))
		}
	}
	ev, err := c.turn(ctx, wire.InstallDependencies{Requirements: requirements}, true, nil)
	if err != nil {
		return err
	}
	if ev.Kind != wire.EventOk {
		c.discard("discarded")
		return protocolError("unexpected reply to InstallDependencies: %s", ev.Kind)
	}
	return nil
}

// Finish ends the checkout, returning a healthy worker to the pool.
func (c *Checkout) Finish(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.lease.active() {
		c.cancelled = false
		c.finishMetrics("error")
		return nil
	}
	if c.inFlight {
		c.discard("abandoned")
		return nil
	}
	if c.worker.Kind() == worker.KindWebSocket {
		if s, won := c.lease.finish(); won {
			c.pool.release(s)
			c.finishMetrics("ok")
		}
		return nil
	}
	ev, err := c.turn(ctx, wire.Reset{}, true, nil)
	if err != nil {
		c.drop("discarded")
		c.finishMetrics("error")
		return err
	}
	if ev.Kind != wire.EventOk {
		c.discard("discarded")
		c.finishMetrics("error")
		return protocolError("unexpected reply to Reset: %s", ev.Kind)
	}
	c.pending = pending{}
	c.feedMounts = nil
	if s, won := c.lease.finish(); won {
		s.served++
		c.pool.release(s)
		c.finishMetrics("ok")
	}
	return nil
}

// Abort ends the pending suspension with exc. The worker MUST answer with an
// Error, returned as a KindRuntime error; the session stays usable.
func (c *Checkout) Abort(ctx context.Context, exc *wire.Exception, onPrint OnPrint) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureReady(); err != nil {
		return err
	}
	if c.pending.kind == pendingNone {
		return protocolError("abort without a pending suspension")
	}
	c.pending = pending{}
	c.abortFlight = true
	ev, err := c.turn(ctx, wire.AbortFeed{Exception: exc}, false, onPrint)
	if err != nil {
		return err
	}
	c.discard("discarded")
	return protocolError("unexpected reply to AbortFeed: %s", ev.Kind)
}

// Terminate kills and retires this checkout's worker without waiting for a
// protocol turn or host callback. It cannot kill an already-released worker.
func (c *Checkout) Terminate(cause error, reason string) bool {
	return c.terminate(cause, reason, true)
}

func (c *Checkout) terminate(cause error, reason string, force bool) bool {
	w, won := c.lease.terminate(cause)
	if !won {
		return false
	}
	killed := force || w.Kind() != worker.KindWebSocket
	if killed {
		w.Kill()
	}
	c.pool.retireLease(w, reason, killed)
	c.finishMetrics(reason)
	c.obs.close()
	return true
}

func (c *Checkout) finishMetrics(outcome string) {
	c.metricsOnce.Do(func() {
		c.pool.cfg.Metrics.SessionDuration(time.Since(c.started), outcome)
	})
}

// HoldObserver keeps observer finalization behind a complete session operation,
// including host callbacks between protocol turns. The release is called once.
func (c *Checkout) HoldObserver() func() {
	if c.obs.acquire() {
		return c.obs.release
	}
	return func() {}
}

// Pending reports whether a suspension awaits an answer.
func (c *Checkout) Pending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending.kind != pendingNone
}

// Done is closed once the worker can no longer serve this checkout.
func (c *Checkout) Done() <-chan struct{} { return c.done }

// WorkerErr reports why the worker ended, when it did.
func (c *Checkout) WorkerErr() error {
	w := c.worker
	if w == nil {
		return nil
	}
	return w.Err()
}

// Abandon kills the worker without resetting it.
func (c *Checkout) Abandon() {
	c.terminate(nil, "abandoned", false)
}

// PendingOsCall returns the OS call awaiting an answer, if any.
func (c *Checkout) PendingOsCall() *wire.OsCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending.kind == pendingCall {
		return c.pending.os
	}
	return nil
}

// HasMounts reports whether the current feed has a mount table.
func (c *Checkout) HasMounts() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.feedMounts != nil
}

func (c *Checkout) send(ctx context.Context, w worker.Worker, req wire.Request, payload []byte) error {
	if err := w.Send(ctx, payload); err != nil {
		return err
	}
	c.obs.sent(req, len(payload))
	return nil
}

// CallbackContext returns ctx carrying the checkout's innermost open telemetry span.
func (c *Checkout) CallbackContext(ctx context.Context) context.Context {
	if c.obs == nil {
		return ctx
	}
	if co, ok := c.obs.o.(ContextObserver); ok {
		return co.CallbackContext(ctx)
	}
	return ctx
}
