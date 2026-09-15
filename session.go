package monty

import (
	"context"
	"errors"
	"os"
	"sort"
	"sync"

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
	mu         sync.Mutex
	closed     bool
	driven     bool
	broken     error
	store      *instanceStore
	scriptName string
}

func (s *Session) ensureUsable() error {
	if s.closed {
		return ErrSessionClosed
	}
	return s.broken
}

func (s *Session) poison(err error) error {
	s.broken = err
	return err
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
		return s.poison(&DisconnectError{Message: perr.Error()})
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
	ans := &answerer{s: s, lookup: opts.ExternalLookup, os: opts.OS, pt: pt, futures: map[uint32]*Future{}}
	ev, err := s.co.Feed(ctx, code, inputs, mounts, first, cwdPtr(opts.Cwd), opts.SkipTypeCheck, pt.onPrint)
	return s.drive(ctx, ev, err, pt, ans)
}

func (s *Session) drive(ctx context.Context, ev *wire.Event, err error, pt *printTarget, ans *answerer) (any, error) {
	for {
		if err != nil {
			var perr *pool.Error
			if errors.As(err, &perr) && (perr.Kind == pool.KindRuntime || perr.Kind == pool.KindTyping) && pt.failure != nil {
				return nil, pt.failure
			}
			var hf *hostFailure
			if errors.As(err, &hf) {
				if s.broken == nil {
					s.broken = hf.err
				}
				return nil, hf.err
			}
			return nil, s.mapError(err)
		}
		if ev.Kind == wire.EventComplete {
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
	ev, err := s.co.Feed(ctx, code, inputs, mounts, first, cwdPtr(opts.Cwd), opts.SkipTypeCheck, d.pt.onPrint)
	return d.advance(ev, err)
}

func (s *Session) newDriver(ctx context.Context, print PrintTarget, lookup map[string]any, os OSHandler) *snapshotDriver {
	pt := newPrintTarget(ctx, s.co, print)
	return &snapshotDriver{s: s, pt: pt, ans: &answerer{s: s, lookup: lookup, os: os, pt: pt, futures: map[uint32]*Future{}}}
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

// Close ends the session and returns its worker to the pool.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	err := s.co.Finish(ctx)
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
