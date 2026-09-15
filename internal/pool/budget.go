package pool

import (
	"time"

	"github.com/asalimonov/montygo/internal/wire"
)

// DefaultMaxSuspensions is the suspension budget when a session sets none.
const DefaultMaxSuspensions = 1000

type sessionBudget struct {
	durationBudget  *time.Duration
	reportedExec    uint64
	suspensionLimit uint64
	suspensionsSeen uint64
}

func (b *sessionBudget) deadline(requestTimeout, grace time.Duration, graceDisabled, control bool) (time.Duration, bool) {
	var d time.Duration
	has := false
	if requestTimeout > 0 {
		d, has = requestTimeout, true
	}
	if !control && b.durationBudget != nil && !graceDisabled {
		rem := *b.durationBudget - time.Duration(b.reportedExec)*time.Microsecond
		if rem < 0 {
			rem = 0
		}
		back := rem + grace
		if !has || back < d {
			d, has = back, true
		}
	}
	return d, has
}

func (b *sessionBudget) observe(ev *wire.Event) {
	if ev.TotalExecutionMicros > b.reportedExec {
		b.reportedExec = ev.TotalExecutionMicros
	}
	if b.durationBudget == nil && ev.MaxDurationMicros != nil {
		d := time.Duration(*ev.MaxDurationMicros) * time.Microsecond
		b.durationBudget = &d
	}
	if ev.MaxSuspensions != nil && *ev.MaxSuspensions < b.suspensionLimit {
		b.suspensionLimit = *ev.MaxSuspensions
	}
	if ev.IsSuspension() {
		b.suspensionsSeen++
	}
}
