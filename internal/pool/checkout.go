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
	slot        *slot
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
}

// Checkout acquires a worker and configures its session.
func (p *Pool) Checkout(ctx context.Context, cfg wire.Configure, opts CheckoutOptions) (*Checkout, error) {
	s, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	cfg.MontyVersion = p.cfg.MontyVersion
	cfg.ProtocolVersion = p.cfg.ProtocolVersion
	c := &Checkout{pool: p, slot: s, started: time.Now()}
	if opts.Observe != nil {
		pid, hasPID := s.w.PID()
		if o := opts.Observe(pid, hasPID); o != nil {
			s.obs = &observer{o: o}
			c.obs = s.obs
		}
	}
	c.budget.suspensionLimit = DefaultMaxSuspensions
	if cfg.Limits != nil {
		if cfg.Limits.MaxDurationMicros != nil {
			d := time.Duration(*cfg.Limits.MaxDurationMicros) * time.Microsecond
			c.budget.durationBudget = &d
		}
		if cfg.Limits.MaxSuspensions != nil {
			c.budget.suspensionLimit = *cfg.Limits.MaxSuspensions
		}
	}
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

// PID returns the worker's process id when it has one and no turn is running.
func (c *Checkout) PID() (int, bool) {
	if !c.mu.TryLock() {
		return 0, false
	}
	defer c.mu.Unlock()
	if c.slot == nil || c.inFlight {
		return 0, false
	}
	return c.slot.w.PID()
}

// Finished reports whether the worker is gone.
func (c *Checkout) Finished() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.slot == nil
}

// Kind returns the transport kind.
func (c *Checkout) Kind() worker.Kind { return c.pool.cfg.Spawner.Kind() }

func (c *Checkout) ensureReady() error {
	if c.cancelled {
		c.cancelled = false
		return &Error{Kind: KindCancelled}
	}
	if c.slot == nil {
		return &Error{Kind: KindFinished}
	}
	if c.inFlight {
		c.discard("discarded")
		return &Error{Kind: KindCancelled}
	}
	return nil
}

func (c *Checkout) drop(reason string) {
	if c.slot != nil {
		c.discard(reason)
	}
}

func (c *Checkout) discard(reason string) {
	s := c.slot
	c.slot = nil
	c.pending = pending{}
	c.feedMounts = nil
	c.inFlight = false
	if s != nil {
		c.pool.discard(s, reason)
	}
}

func (c *Checkout) turn(ctx context.Context, req wire.Request, control bool, onPrint OnPrint) (*wire.Event, error) {
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
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
	w := c.slot.w
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
	s := c.slot
	c.slot = nil
	c.pending = pending{}
	c.feedMounts = nil
	c.inFlight = false
	if s != nil {
		if s.w.Kind() != worker.KindWebSocket {
			s.w.Kill()
			wctx, cancel := context.WithTimeout(context.Background(), time.Second)
			s.w.Wait(wctx)
			cancel()
		}
		c.pool.discard(s, "turn_timeout")
	}
	return &Error{Kind: KindTimeout, Timeout: deadline, WorkerLost: true}
}

func (c *Checkout) poison(doing string) error {
	s := c.slot
	c.slot = nil
	c.pending = pending{}
	c.feedMounts = nil
	c.inFlight = false
	if s == nil {
		return &Error{Kind: KindFinished}
	}
	if s.w.Kind() == worker.KindWebSocket {
		c.pool.discard(s, "disconnected")
		return &Error{Kind: KindDisconnected, Message: doing, WorkerLost: true}
	}
	status := reap(s.w, fatalExitGrace)
	if status.Exited && status.Code == 65 {
		c.pool.discard(s, "oom")
		return &Error{Kind: KindRuntime, Exception: wire.NewException("MemoryError", "the worker exceeded its memory limit and was terminated"), WorkerLost: true}
	}
	c.pool.discard(s, "crash")
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
		s := c.slot
		c.slot = nil
		c.pending = pending{}
		c.feedMounts = nil
		status := reap(s.w, fatalExitGrace)
		c.pool.discard(s, "fatal")
		return nil, &Error{Kind: KindCrashed, Announced: true, Message: ev.FatalMessage, Status: status, WorkerLost: true}
	case wire.EventShutdown:
		if c.slot.w.Kind() != worker.KindWebSocket {
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureReady(); err != nil {
		return nil, false, err
	}
	if c.pending.kind != pendingCall {
		return nil, false, protocolError("no suspended call to resume")
	}
	if c.pending.os == nil {
		return nil, false, protocolError("resume_from_mounts is only valid answering an OS call")
	}
	if c.feedMounts == nil {
		return nil, false, nil
	}
	c.inFlight = true
	handled, result, exc := c.feedMounts.HandleOsCall(ctx, c.pending.os)
	c.inFlight = false
	if !handled {
		return nil, false, nil
	}
	res := wire.ExtResult{Kind: wire.ExtReturn, Value: result}
	if exc != nil {
		res = wire.ExtResult{Kind: wire.ExtError, Error: exc}
	}
	ev, err := c.resumeLocked(ctx, res, onPrint)
	var perr *Error
	if errors.As(err, &perr) && perr.Kind == KindRuntime && perr.PreSend && c.pending.kind == pendingCall {
		ev, err = c.resumeLocked(ctx, wire.ExtResult{Kind: wire.ExtError, Error: perr.Exception}, onPrint)
	}
	return ev, true, err
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
	if c.slot == nil {
		c.cancelled = false
		c.pool.cfg.Metrics.SessionDuration(time.Since(c.started), "error")
		return nil
	}
	if c.inFlight {
		c.discard("abandoned")
		return nil
	}
	s := c.slot
	if s.w.Kind() == worker.KindWebSocket {
		c.slot = nil
		c.pool.release(s)
		c.pool.cfg.Metrics.SessionDuration(time.Since(c.started), "ok")
		return nil
	}
	ev, err := c.turn(ctx, wire.Reset{}, true, nil)
	if err != nil {
		c.drop("discarded")
		c.pool.cfg.Metrics.SessionDuration(time.Since(c.started), "error")
		return err
	}
	if ev.Kind != wire.EventOk {
		c.discard("discarded")
		c.pool.cfg.Metrics.SessionDuration(time.Since(c.started), "error")
		return protocolError("unexpected reply to Reset: %s", ev.Kind)
	}
	c.slot = nil
	c.pending = pending{}
	c.feedMounts = nil
	s.served++
	c.pool.release(s)
	c.pool.cfg.Metrics.SessionDuration(time.Since(c.started), "ok")
	return nil
}

// Abandon kills the worker without resetting it.
func (c *Checkout) Abandon() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.slot != nil {
		c.discard("abandoned")
		c.pool.cfg.Metrics.SessionDuration(time.Since(c.started), "abandoned")
	}
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
