package montygo

import (
	"context"
	"errors"
	"os"
	"sort"

	"github.com/asalimonov/montygo/internal/pool"
	"github.com/asalimonov/montygo/internal/wire"
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
	driven     bool
	store      *instanceStore
	scriptName string
	host       *Host
	limits     sessionLimits
	life       lifecycle
}

func (s *Session) ensureUsable() error { return s.Err() }

func (s *Session) poison(err error) error { return s.terminateSession(err) }

// Done closes when this session becomes terminal, independently of host callbacks.
func (s *Session) Done() <-chan struct{} { return s.life.done }

// Err is the canonical terminal cause; nil does not reserve execution admission.
func (s *Session) Err() error {
	s.life.mu.Lock()
	defer s.life.mu.Unlock()
	return s.life.terminal
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
	return SessionStats{HostObjects: count, PeakHostObjects: peak, PendingFutures: s.pendingCount()}
}

// CloseNow fences the session and retires its worker without joining callbacks.
func (s *Session) CloseNow() error {
	_ = s.terminateSession(ErrSessionClosed)
	return nil
}

// mapError converts a pool failure, poisoning the session when it is lost.
func (s *Session) mapError(err error) error {
	var aborted *abortedTurn
	if errors.As(err, &aborted) {
		err = aborted.cause
	}
	var perr *pool.Error
	if !errors.As(err, &perr) {
		if errors.Is(err, ErrSessionLost) {
			return s.poison(err)
		}
		return err
	}
	if terminal := s.Err(); terminal != nil {
		return terminal
	}
	switch perr.Kind {
	case pool.KindRuntime:
		result := errorFromException(perr.Exception)
		if perr.WorkerLost {
			if re, ok := result.(*RuntimeError); ok {
				re.lost = true
			}
			return s.poison(result)
		}
		return result
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
			return s.poison(&ProtocolError{Message: perr.Error(), cause: errors.Join(ErrTurnCancelled, perr.Cause)})
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

// FeedRun executes one snippet. An overlapping operation returns ErrSessionBusy.
func (s *Session) FeedRun(ctx context.Context, code string, opts *FeedOptions) (any, error) {
	e, err := s.reserveExecution(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.feedRun(ctx, e, code, copyFeedOptions(opts))
	s.finishExecution(e, v, err)
	return e.value, e.err
}

func (s *Session) feedRun(ctx context.Context, e *execution, code string, opts *FeedOptions) (any, error) {
	if err := s.beginExecution(e); err != nil {
		return nil, err
	}
	inputs, err := s.prepareInputs(opts.Inputs)
	if err != nil {
		return nil, err
	}
	mounts, first, err := buildMounts(opts.Mount)
	if err != nil {
		return nil, err
	}
	pt := newPrintTarget(ctx, s.co, opts.Print)
	pt.exec = e
	e.print = pt
	ans := s.newAnswerer(e, opts.ExternalLookup, opts.OS, pt)
	if err := s.beginSend(e); err != nil {
		return nil, err
	}
	ev, err := s.co.Feed(ctx, code, inputs, mounts, first, cwdPtr(opts.Cwd), opts.SkipTypeCheck, pt.onPrint)
	return s.drive(ctx, e, ev, err, pt, ans)
}

func (s *Session) newAnswerer(e *execution, lookup map[string]any, os OSHandler, pt *printTarget) *answerer {
	return &answerer{s: s, exec: e, lookup: lookup, host: s.host, os: os, pt: pt}
}

func (s *Session) drive(ctx context.Context, e *execution, ev *wire.Event, err error, pt *printTarget, ans *answerer) (any, error) {
	for {
		s.receivedTurn(e)
		if terminal := s.Err(); terminal != nil {
			return nil, terminal
		}
		if err != nil {
			var perr *pool.Error
			if errors.As(err, &perr) && (perr.Kind == pool.KindRuntime || perr.Kind == pool.KindTyping) || e.aborted {
				if ferr := pt.finish(); ferr != nil {
					return nil, ferr
				}
				if pt.failure != nil {
					return nil, pt.failure
				}
			}
			var hf *hostFailure
			if errors.As(err, &hf) {
				return nil, s.poison(hf.err)
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

// FeedStart starts a snippet and retains ownership across its snapshot chain.
func (s *Session) FeedStart(ctx context.Context, code string, opts *FeedOptions) (snap Snapshot, err error) {
	e, err := s.reserveExecution(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			s.finishExecution(e, nil, err)
		}
	}()
	if err = s.beginExecution(e); err != nil {
		return nil, err
	}
	opts = copyFeedOptions(opts)
	inputs, err := s.prepareInputs(opts.Inputs)
	if err != nil {
		return nil, err
	}
	mounts, first, err := buildMounts(opts.Mount)
	if err != nil {
		return nil, err
	}
	d := s.newDriver(e, ctx, opts.Print, opts.ExternalLookup, opts.OS)
	if err = s.beginSend(e); err != nil {
		return nil, err
	}
	ev, err := s.co.Feed(ctx, code, inputs, mounts, first, cwdPtr(opts.Cwd), opts.SkipTypeCheck, d.pt.onPrint)
	return d.advance(ev, err)
}

func (s *Session) newDriver(e *execution, ctx context.Context, print PrintTarget, lookup map[string]any, os OSHandler) *snapshotDriver {
	pt := newPrintTarget(ctx, s.co, print)
	pt.exec = e
	e.print = pt
	return &snapshotDriver{s: s, exec: e, pt: pt, ans: s.newAnswerer(e, lookup, os, pt)}
}

func (s *Session) claimFresh() error {
	if err := s.ensureUsable(); err != nil {
		return err
	}
	if s.driven {
		return ErrNotFresh
	}
	return nil
}

func (s *Session) failedLoad(err error) error { return s.poison(err) }

// LoadSession restores an idle dump into a fresh session.
func (s *Session) LoadSession(ctx context.Context, state []byte) error {
	release, err := s.reserveControl(ctx, false)
	if err != nil {
		return err
	}
	defer release()
	if s.host != nil {
		if err := s.host.Restorable(); err != nil {
			return err
		}
	}
	if err := s.claimFresh(); err != nil {
		return err
	}
	s.driven = true
	pt := newPrintTarget(ctx, s.co, nil)
	ev, _, err := s.co.Restore(ctx, state, nil, pt.onPrint)
	if err != nil {
		return s.failedLoad(s.mapError(err))
	}
	if ev != nil {
		return s.failedLoad(ErrDumpIsSuspended)
	}
	return nil
}

// LoadSnapshot restores a suspended dump into a fresh session.
func (s *Session) LoadSnapshot(ctx context.Context, state []byte, opts *LoadSnapshotOptions) (snap Snapshot, err error) {
	e, err := s.reserveExecution(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			s.finishExecution(e, nil, err)
		}
	}()
	if s.host != nil {
		if err := s.host.Restorable(); err != nil {
			return nil, err
		}
	}
	if err = s.claimFresh(); err != nil {
		return nil, err
	}
	if err = s.beginExecution(e); err != nil {
		return nil, err
	}
	if opts == nil {
		opts = &LoadSnapshotOptions{}
	}
	mounts, _, err := buildMounts(opts.Mount)
	if err != nil {
		return nil, err
	}
	d := s.newDriver(e, ctx, opts.Print, opts.ExternalLookup, opts.OS)
	if err = s.beginSend(e); err != nil {
		return nil, err
	}
	ev, _, err := s.co.Restore(ctx, state, mounts, d.pt.onPrint)
	if err != nil {
		return nil, s.failedLoad(s.mapError(err))
	}
	if ev == nil {
		return nil, s.failedLoad(ErrDumpIsIdle)
	}
	return d.advance(ev, nil)
}

// Dump serializes an idle or paused session; a running turn returns ErrSessionBusy.
func (s *Session) Dump(ctx context.Context) ([]byte, error) {
	release, err := s.reserveControl(ctx, true)
	if err != nil {
		return nil, err
	}
	defer release()
	state, err := s.co.Dump(ctx)
	if err != nil {
		return nil, s.mapError(err)
	}
	return state, nil
}

// InstallDependencies installs packages into a CPython worker's session.
func (s *Session) InstallDependencies(ctx context.Context, requirements []string) error {
	release, err := s.reserveControl(ctx, false)
	if err != nil {
		return err
	}
	defer release()
	s.driven = true
	return s.mapError(s.co.InstallDependencies(ctx, requirements))
}

// WorkerPID reports the worker process ID when no turn is running.
func (s *Session) WorkerPID() (int, bool) { return s.co.PID() }

// ScriptName reports the session's traceback name.
func (s *Session) ScriptName() string { return s.scriptName }

type printTarget struct {
	exec    *execution
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
		cb := p.co.CallbackContext(p.ctx)
		if p.exec != nil {
			cb = callbackContext{Context: p.exec.callbackCtx, values: cb}
		}
		err = ct.PrintContext(cb, st, text)
	} else {
		err = p.target.Print(st, text)
	}
	if err != nil {
		p.failure = err
	}
}
