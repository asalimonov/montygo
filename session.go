package montygo

import (
	"context"
	"errors"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/wire"
	"github.com/asalimonov/montygo/internal/worker"
)

// FeedOptions configure one snippet.
type FeedOptions struct {
	// Inputs are bound as globals before the snippet runs.
	Inputs map[string]any
	// ExternalLookup resolves undefined names lazily: functions become host
	// functions, other values are returned directly, absent names raise NameError.
	ExternalLookup map[string]any
	// Print receives output; nil writes to the process stdout/stderr.
	Print PrintTarget
	Mount []*MountDir
	// Cwd switches the sandbox working directory (absolute virtual path).
	Cwd string
	// OS answers OS calls no mount covered.
	OS            OSHandler
	SkipTypeCheck bool
}

// LoadSnapshotOptions configure LoadSnapshot.
type LoadSnapshotOptions struct {
	Print          PrintTarget
	Mount          []*MountDir
	ExternalLookup map[string]any
	OS             OSHandler
}

// Session is one worker dedicated to one REPL session.
type Session struct {
	pool       *Pool
	co         *pool.Checkout
	mu         sync.Mutex
	closed     bool
	driven     bool
	broken     error
	store      *instanceStore
	scriptName string
	host       *Host
	limits     sessionLimits
	life       lifecycle
}

// lifecycle is the state Interrupt, CloseNow, Done and Err share without
// taking the session mutex a running feed holds.
type lifecycle struct {
	mu        sync.Mutex
	done      chan struct{}
	err       error
	interrupt error
	// feedCancel ends the feed context host calls and AsyncContext futures derive from.
	feedCancel context.CancelFunc
	feedCtx    context.Context
	// hostBusy is set while a host call runs or its abort is in flight.
	hostBusy bool
	inFeed   bool
	feedEnd  chan struct{}
	closing  bool
	aborted  error
	pending  map[*Future]struct{}
}

func newSession(p *Pool, scriptName string, limits sessionLimits) *Session {
	s := &Session{pool: p, store: newInstanceStore(limits.hostObjects), scriptName: scriptName, limits: limits}
	s.life.done = make(chan struct{})
	s.life.feedEnd = make(chan struct{})
	close(s.life.feedEnd)
	return s
}

// attach binds the checkout and watches its worker so a session lost while idle is reported.
func (s *Session) attach(co *pool.Checkout) {
	s.co = co
	go func() {
		<-co.Done()
		if s.life.ended() {
			return
		}
		var err error
		if s.pool.backend == BackendWebSocket {
			de := &DisconnectError{Message: "monty worker connection closed while idle"}
			var closed *worker.ClosedError
			if errors.As(co.WorkerErr(), &closed) {
				de.Code, de.Reason = closed.Code, closed.Reason
				de.Message += ": " + closed.Error()
			}
			err = de
		} else {
			err = &CrashedError{Message: "monty worker crashed while idle"}
		}
		s.life.finish(err)
	}()
}

func (l *lifecycle) ended() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err != nil
}

// finish records the terminal error once and closes Done.
func (l *lifecycle) finish(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return
	}
	l.err = err
	close(l.done)
	for f := range l.pending {
		f.settle(nil, ErrSessionLost)
	}
	l.pending = nil
}

func (l *lifecycle) beginFeed(ctx context.Context) {
	l.mu.Lock()
	l.inFeed = true
	l.interrupt = nil
	l.aborted = nil
	l.feedCtx, l.feedCancel = context.WithCancel(ctx)
	l.feedEnd = make(chan struct{})
	l.mu.Unlock()
}

func (l *lifecycle) endFeed() {
	l.mu.Lock()
	l.inFeed = false
	l.hostBusy = false
	l.feedCancel()
	close(l.feedEnd)
	l.mu.Unlock()
}

// beginCallback returns the context of a host call: the feed context for
// cancellation, cbCtx for values. It reports an interrupt that arrived before the call.
func (l *lifecycle) beginCallback(cbCtx context.Context) (context.Context, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.interrupt != nil {
		return nil, l.interrupt
	}
	l.hostBusy = true
	return callbackContext{Context: l.feedCtx, values: cbCtx}, nil
}

func (l *lifecycle) endCallback() {
	l.mu.Lock()
	l.hostBusy = false
	l.mu.Unlock()
}

// callbackContext follows the feed context and reads values from the turn context.
type callbackContext struct {
	context.Context
	values context.Context
}

func (c callbackContext) Value(key any) any {
	if v := c.values.Value(key); v != nil {
		return v
	}
	return c.Context.Value(key)
}

func (l *lifecycle) takeInterrupt() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	reason := l.interrupt
	l.interrupt = nil
	return reason
}

func (l *lifecycle) isClosing() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closing
}

func (l *lifecycle) addPending(f *Future) {
	l.mu.Lock()
	if l.pending == nil {
		l.pending = map[*Future]struct{}{}
	}
	l.pending[f] = struct{}{}
	l.mu.Unlock()
}

func (l *lifecycle) removePending(f *Future) {
	l.mu.Lock()
	delete(l.pending, f)
	l.mu.Unlock()
}

func (l *lifecycle) pendingCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.pending)
}

func (s *Session) ensureUsable() error {
	if s.closed {
		return ErrSessionClosed
	}
	if s.broken != nil {
		return s.broken
	}
	if s.life.isClosing() {
		return ErrSessionClosed
	}
	if s.life.ended() {
		return s.life.err
	}
	return nil
}

func (s *Session) poison(err error) error {
	s.broken = err
	s.life.finish(err)
	s.pool.untrack(s)
	return err
}

// Done is closed when the session is closed, lost or its worker ended.
func (s *Session) Done() <-chan struct{} { return s.life.done }

// Err reports why the session is unusable; nil while it is usable.
func (s *Session) Err() error {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	return s.life.err
}

// SessionStats are host-side counters of one session.
type SessionStats struct {
	HostObjects     int
	PeakHostObjects int
	PendingFutures  int
}

// Stats reports the session's host-side counters.
func (s *Session) Stats() SessionStats {
	count, peak := s.store.stats()
	return SessionStats{HostObjects: count, PeakHostObjects: peak, PendingFutures: s.life.pendingCount()}
}

var keyboardInterrupt = Raise("KeyboardInterrupt", "")

// Interrupt stops the running feed. While the worker waits on a host call, the
// call's context is cancelled and the feed ends inside the sandbox with a
// KeyboardInterrupt (or reason); the session stays usable. While Python is
// executing, the worker is killed after InterruptGrace and the session is lost.
// A suspended snapshot is aborted at once. Safe to call from any goroutine;
// nil when nothing is running.
func (s *Session) Interrupt(ctx context.Context, reason error) error {
	if reason == nil {
		reason = keyboardInterrupt
	}
	l := &s.life
	l.mu.Lock()
	if !l.inFeed {
		l.mu.Unlock()
		return s.abortSuspended(ctx, reason)
	}
	l.interrupt = reason
	busy, feedEnd := l.hostBusy, l.feedEnd
	l.feedCancel()
	l.mu.Unlock()
	if busy {
		return waitClosed(ctx, feedEnd)
	}
	timer := time.NewTimer(s.limits.interruptGrace)
	defer timer.Stop()
	select {
	case <-feedEnd:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	l.mu.Lock()
	busy = l.hostBusy
	l.mu.Unlock()
	if busy {
		return waitClosed(ctx, feedEnd)
	}
	s.co.KillWorker()
	return waitClosed(ctx, feedEnd)
}

func waitClosed(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// abortSuspended ends a suspension a snapshot left pending.
func (s *Session) abortSuspended(ctx context.Context, reason error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureUsable(); err != nil || !s.co.Pending() {
		return nil
	}
	actx, cancel := s.abortContext(ctx)
	defer cancel()
	excType, msg := exceptionParts(reason)
	err := s.mapError(s.co.Abort(actx, wire.NewException(excType, msg), nil))
	var re *RuntimeError
	if errors.As(err, &re) {
		s.life.mu.Lock()
		s.life.aborted = err
		s.life.mu.Unlock()
		return nil
	}
	return err
}

const abortDeadline = 5 * time.Second

// abortContext bounds an AbortFeed turn independently of a cancelled feed context.
func (s *Session) abortContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), abortDeadline)
}

// CloseNow ends the session at once: a running feed returns ErrSessionClosed
// and the worker is killed. Close afterwards is a no-op.
func (s *Session) CloseNow() error {
	l := &s.life
	l.mu.Lock()
	if l.closing {
		l.mu.Unlock()
		return nil
	}
	l.closing = true
	if l.inFeed {
		l.feedCancel()
	}
	l.mu.Unlock()
	l.finish(ErrSessionClosed)
	s.co.KillWorker()
	s.pool.untrack(s)
	return nil
}

// mapError converts a pool failure, poisoning the session when it is lost.
func (s *Session) mapError(err error) error {
	var perr *pool.Error
	if !errors.As(err, &perr) {
		return err
	}
	switch perr.Kind {
	case pool.KindRuntime:
		return errorFromException(perr.Exception)
	case pool.KindTyping:
		return &TypingError{Diagnostics: perr.Diagnostics}
	case pool.KindTimeout:
		return s.poison(&CrashedError{Message: perr.Error(), TimedOut: true})
	case pool.KindCrashed:
		return s.poison(&CrashedError{Message: perr.Error(), ExitStatus: perr.Status.String()})
	case pool.KindDisconnected:
		return s.poison(&DisconnectError{Message: perr.Error(), Code: perr.CloseCode, Reason: perr.CloseReason})
	case pool.KindShutdown:
		return s.poison(&ShutdownError{Message: perr.Error(), Dump: perr.Dump})
	case pool.KindCancelled:
		if perr.Cause != nil {
			s.broken = &ProtocolError{Message: perr.Error(), cause: ErrTurnCancelled}
			return perr.Cause
		}
		return s.poison(&ProtocolError{Message: perr.Error(), cause: ErrTurnCancelled})
	}
	return s.poison(&ProtocolError{Message: perr.Error()})
}

func (s *Session) prepareInputs(inputs map[string]any) ([]wire.NamedValue, error) {
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]wire.NamedValue, 0, len(names))
	for _, name := range names {
		v, err := prepareValue(inputs[name], s.store)
		if err != nil {
			return nil, err
		}
		out = append(out, wire.NamedValue{Name: name, Value: v})
	}
	return out, nil
}

func cwdPtr(cwd string) *string {
	if cwd == "" {
		return nil
	}
	return &cwd
}

// FeedRun executes a snippet, answering external calls, OS calls and name
// lookups on the host, and returns the snippet's trailing expression value.
func (s *Session) FeedRun(ctx context.Context, code string, opts *FeedOptions) (any, error) {
	if opts == nil {
		opts = &FeedOptions{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureUsable(); err != nil {
		return nil, err
	}
	s.driven = true
	inputs, err := s.prepareInputs(opts.Inputs)
	if err != nil {
		return nil, err
	}
	mounts, first, err := buildMounts(opts.Mount)
	if err != nil {
		return nil, err
	}
	pt := newPrintTarget(ctx, s.co, opts.Print)
	ans := s.newAnswerer(opts.ExternalLookup, opts.OS, pt)
	s.life.beginFeed(ctx)
	defer s.life.endFeed()
	ev, err := s.co.Feed(ctx, code, inputs, mounts, first, cwdPtr(opts.Cwd), opts.SkipTypeCheck, pt.onPrint)
	return s.drive(ctx, ev, err, pt, ans)
}

func (s *Session) newAnswerer(lookup map[string]any, os OSHandler, pt *printTarget) *answerer {
	return &answerer{s: s, lookup: lookup, host: s.host, os: os, pt: pt, futures: map[uint32]*Future{}}
}

func (s *Session) drive(ctx context.Context, ev *wire.Event, err error, pt *printTarget, ans *answerer) (any, error) {
	for {
		if err != nil {
			if s.life.isClosing() {
				return nil, ErrSessionClosed
			}
			var perr *pool.Error
			if errors.As(err, &perr) && (perr.Kind == pool.KindRuntime || perr.Kind == pool.KindTyping) {
				if ferr := pt.finish(); ferr != nil {
					return nil, ferr
				}
				if pt.failure != nil {
					return nil, pt.failure
				}
			}
			var hf *hostFailure
			if errors.As(err, &hf) {
				if s.broken == nil {
					return nil, s.poison(hf.err)
				}
				return nil, hf.err
			}
			return nil, s.mapError(err)
		}
		if ev.Kind == wire.EventComplete {
			if ferr := pt.finish(); ferr != nil {
				return nil, ferr
			}
			if pt.failure != nil {
				return nil, pt.failure
			}
			return restoreValue(ev.Value, s.store), nil
		}
		if pt.failure != nil {
			return nil, s.poison(pt.failure)
		}
		ev, err = ans.answer(ctx, ev)
	}
}

// FeedStart starts a snippet and returns a snapshot at its first suspension.
func (s *Session) FeedStart(ctx context.Context, code string, opts *FeedOptions) (Snapshot, error) {
	if opts == nil {
		opts = &FeedOptions{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureUsable(); err != nil {
		return nil, err
	}
	s.driven = true
	inputs, err := s.prepareInputs(opts.Inputs)
	if err != nil {
		return nil, err
	}
	mounts, first, err := buildMounts(opts.Mount)
	if err != nil {
		return nil, err
	}
	d := s.newDriver(ctx, opts.Print, opts.ExternalLookup, opts.OS)
	s.life.beginFeed(ctx)
	defer s.life.endFeed()
	ev, err := s.co.Feed(ctx, code, inputs, mounts, first, cwdPtr(opts.Cwd), opts.SkipTypeCheck, d.pt.onPrint)
	return d.advance(ev, err)
}

func (s *Session) newDriver(ctx context.Context, print PrintTarget, lookup map[string]any, os OSHandler) *snapshotDriver {
	pt := newPrintTarget(ctx, s.co, print)
	return &snapshotDriver{s: s, pt: pt, ans: s.newAnswerer(lookup, os, pt)}
}

func (s *Session) claimFresh() error {
	if err := s.ensureUsable(); err != nil {
		return err
	}
	if s.driven {
		return ErrNotFresh
	}
	s.driven = true
	return nil
}

func (s *Session) failedLoad(err error) error {
	if s.broken == nil {
		s.broken = err
	}
	_ = s.co.Finish(context.Background())
	return err
}

// LoadSession restores an idle session dump into this fresh session.
func (s *Session) LoadSession(ctx context.Context, state []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.host != nil {
		if err := s.host.Restorable(); err != nil {
			return err
		}
	}
	if err := s.claimFresh(); err != nil {
		return err
	}
	pt := newPrintTarget(ctx, s.co, nil)
	ev, _, err := s.co.Restore(ctx, state, nil, pt.onPrint)
	if err != nil {
		mapped := s.mapError(err)
		return s.failedLoad(mapped)
	}
	if ev != nil {
		return s.failedLoad(ErrDumpIsSuspended)
	}
	return nil
}

// LoadSnapshot restores a suspended dump and returns the snapshot to resume.
func (s *Session) LoadSnapshot(ctx context.Context, state []byte, opts *LoadSnapshotOptions) (Snapshot, error) {
	if opts == nil {
		opts = &LoadSnapshotOptions{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.claimFresh(); err != nil {
		return nil, err
	}
	mounts, _, err := buildMounts(opts.Mount)
	if err != nil {
		return nil, s.failedLoad(err)
	}
	d := s.newDriver(ctx, opts.Print, opts.ExternalLookup, opts.OS)
	ev, _, err := s.co.Restore(ctx, state, mounts, d.pt.onPrint)
	if err != nil {
		return nil, s.failedLoad(s.mapError(err))
	}
	if ev == nil {
		return nil, s.failedLoad(ErrDumpIsIdle)
	}
	snap, err := d.advance(ev, nil)
	if err != nil {
		return nil, s.failedLoad(err)
	}
	return snap, nil
}

// Dump serializes the session; it stays usable.
func (s *Session) Dump(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureUsable(); err != nil {
		return nil, err
	}
	state, err := s.co.Dump(ctx)
	if err != nil {
		return nil, s.mapError(err)
	}
	return state, nil
}

// InstallDependencies installs packages into a CPython worker's session.
func (s *Session) InstallDependencies(ctx context.Context, requirements []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureUsable(); err != nil {
		return err
	}
	s.driven = true
	if err := s.co.InstallDependencies(ctx, requirements); err != nil {
		return s.mapError(err)
	}
	return nil
}

// WorkerPID is the worker's process id; false for non-process workers or while a turn runs.
func (s *Session) WorkerPID() (int, bool) { return s.co.PID() }

// ScriptName is the session's script name.
func (s *Session) ScriptName() string { return s.scriptName }

// Close ends the session and returns its worker to the pool. It waits for a
// running call to finish; CloseNow does not.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.pool.untrack(s)
	err := s.co.Finish(ctx)
	s.life.finish(ErrSessionClosed)
	if err != nil {
		return s.mapError(err)
	}
	return nil
}

type printTarget struct {
	ctx     context.Context
	co      *pool.Checkout
	target  PrintTarget
	failure error
}

// finish flushes a buffering target at the end of a turn.
func (p *printTarget) finish() error {
	f, ok := p.target.(FlushingPrintTarget)
	if !ok || p.failure != nil {
		return nil
	}
	if err := f.Flush(); err != nil {
		p.failure = err
		return err
	}
	return nil
}

func newPrintTarget(ctx context.Context, co *pool.Checkout, target PrintTarget) *printTarget {
	return &printTarget{ctx: ctx, co: co, target: target}
}

func (p *printTarget) onPrint(stream uint8, text string) {
	if p.failure != nil {
		return
	}
	st := Stdout
	if stream == 2 {
		st = Stderr
	}
	if p.target == nil {
		if st == Stdout {
			_, _ = os.Stdout.WriteString(text)
		} else {
			_, _ = os.Stderr.WriteString(text)
		}
		return
	}
	defer func() {
		if r := recover(); r != nil {
			p.failure = panicError(r)
		}
	}()
	var err error
	if ct, ok := p.target.(ContextPrintTarget); ok {
		err = ct.PrintContext(p.co.CallbackContext(p.ctx), st, text)
	} else {
		err = p.target.Print(st, text)
	}
	if err != nil {
		p.failure = err
	}
}
