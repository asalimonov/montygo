package montygo

import (
	"context"
	monterr "github.com/asalimonov/montygo/monterr"
	"sync"
	"time"
)

// defaultRotationMargin is the lead time before a server's session deadline.
const defaultRotationMargin = 30 * time.Second

// rotationPolicy moves a session to a fresh connection before the server closes
// it. A server bounds one execution by its turn timeout, so a session that only
// starts work while more than turnTimeout+margin remains never has an execution
// killed by the session deadline.
type rotationPolicy struct {
	sessionTimeout time.Duration
	turnTimeout    time.Duration
	margin         time.Duration
}

// newRotationPolicy returns nil when the server reports limits that cannot be
// rotated around: a reported deadline shorter than two lead times would rotate
// continuously, so rotation stays off instead.
func newRotationPolicy(info *ServerInfo, margin time.Duration) *rotationPolicy {
	if info == nil {
		return nil
	}
	if margin == 0 {
		margin = defaultRotationMargin
	}
	l := info.Limits
	if l.SessionTimeout <= 0 || l.TurnTimeout <= 0 || l.SessionTimeout < 2*(l.TurnTimeout+margin) {
		return nil
	}
	return &rotationPolicy{sessionTimeout: l.SessionTimeout, turnTimeout: l.TurnTimeout, margin: margin}
}

func (r *rotationPolicy) due(deadline, now time.Time) bool {
	return deadline.Sub(now) <= r.turnTimeout+r.margin
}

// connection is what one dial gives a session: when the server will close it,
// how to stop watching it, and the timer that rotates it while it stays idle.
type connection struct {
	mu        sync.Mutex
	deadline  time.Time
	stopWatch func()
	timer     *time.Timer
	// rotating is open while a rotation runs, so a caller waits for it instead
	// of finding the session busy with work it never asked for.
	rotating chan struct{}
}

// beginRotation publishes an in-flight rotation and returns its completion.
func (c *connection) beginRotation() func() {
	ch := make(chan struct{})
	c.mu.Lock()
	c.rotating = ch
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		if c.rotating == ch {
			c.rotating = nil
		}
		c.mu.Unlock()
		close(ch)
	}
}

func (c *connection) inFlight() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rotating
}

func (c *connection) get() (time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deadline, c.stopWatch
}

func (c *connection) set(deadline time.Time, stopWatch func(), timer *time.Timer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
	}
	c.deadline, c.stopWatch, c.timer = deadline, stopWatch, timer
}

func (c *connection) disarm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}

// armRotation records the new connection's deadline and schedules the idle
// rotation. dialStart is taken before the dial, so the client's deadline is
// never later than the server's.
func (s *Session) armRotation(dialStart time.Time, stopWatch func()) {
	r := s.pool.rotation
	if r == nil {
		s.conn.set(time.Time{}, stopWatch, nil)
		return
	}
	deadline := dialStart.Add(r.sessionTimeout)
	timer := time.AfterFunc(time.Until(deadline.Add(-r.margin)), s.rotateOnTimer)
	s.conn.set(deadline, stopWatch, timer)
}

// rotateIfDue moves the session to a fresh connection before an operation that
// the server's session deadline would otherwise cut short.
func (s *Session) rotateIfDue(ctx context.Context) error {
	r := s.pool.rotation
	if r == nil {
		return nil
	}
	if err := s.awaitRotation(ctx); err != nil {
		return err
	}
	deadline, _ := s.conn.get()
	if !r.due(deadline, time.Now()) {
		return nil
	}
	release, err := s.reserveControl(ctx, false)
	if err != nil {
		// A busy or terminal session reports itself when the operation is admitted.
		return nil
	}
	defer release()
	if deadline, _ := s.conn.get(); !r.due(deadline, time.Now()) {
		return nil
	}
	return s.rotateLocked(ctx)
}

// awaitRotation waits for a rotation started elsewhere, so an operation queues
// behind it rather than failing. A rotation that ended the session reports it.
func (s *Session) awaitRotation(ctx context.Context) error {
	for {
		ch := s.conn.inFlight()
		if ch == nil {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		case <-s.Done():
			return s.Err()
		}
	}
}

// rotateOnTimer rotates a session that has stayed idle towards its deadline.
func (s *Session) rotateOnTimer() {
	r := s.pool.rotation
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.turnTimeout+r.margin)
	defer cancel()
	release, err := s.reserveControl(ctx, false)
	if err != nil {
		return
	}
	defer release()
	_ = s.rotateLocked(ctx)
}

// rotateLocked replaces the session's worker under a control reservation: the
// sandbox state moves as a dump, and the capacity slot stays with the session.
func (s *Session) rotateLocked(ctx context.Context) error {
	done := s.conn.beginRotation()
	defer done()
	old := s.co
	state, err := old.Dump(ctx)
	if err != nil {
		// NotImplemented: a session whose signed dump exceeds the 256 MiB frame
		// limit cannot be moved, so it ends here with no state to hand back.
		return s.failRotation(nil, "dump before rotation failed", err)
	}
	if _, stop := s.conn.get(); stop != nil {
		stop()
	}
	res, err := old.Handoff()
	if err != nil {
		return s.failRotation(state, "handoff before rotation failed", err)
	}
	co, dialStart, err := s.pool.bind(ctx, res, s.cfg, state)
	if err != nil {
		res.Release()
		return s.failRotation(state, "reconnect during rotation failed", err)
	}
	s.attach(co, dialStart)
	return nil
}

func (s *Session) failRotation(dump []byte, doing string, cause error) error {
	err := &monterr.RotationError{Message: doing + ": " + cause.Error(), Dump: dump, Cause: cause}
	_ = s.terminateSession(err)
	return err
}
