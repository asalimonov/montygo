package telemetry

import (
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/asalimonov/montygo/internal/wire"
)

type suspension struct {
	start    time.Time
	kind     string
	function string
	eager    bool
	callID   uint32
}

// TurnMetrics mirrors one checkout's protocol turns into per-turn metrics.
// Only protocol-fixed labels become attributes, never a sandbox-chosen name.
type TurnMetrics struct {
	feedStart time.Time
	feed      bool
	runActive bool
	pending   *suspension
	turn      string
	turnStart time.Time
	reported  uint64
}

var printStreams = [...]string{"stdout", "stderr", "unspecified"}

// Sent records a request that reached the wire.
func (t *TurnMetrics) Sent(req wire.Request, frameLen int) {
	record(FrameBytes, float64(frameLen), attribute.String("direction", "sent"))
	now := time.Now()
	switch r := req.(type) {
	case wire.Configure:
		t.feed, t.runActive = false, false
		t.abandonPending()
		t.turn, t.turnStart = "configure", now
		t.reported = 0
	case wire.Feed:
		t.abandonPending()
		t.feed, t.feedStart = true, now
		t.runActive = true
	case wire.Load:
		t.runActive = false
		t.reported = 0
		t.turn, t.turnStart = "load", now
	case wire.Dump:
		t.turn, t.turnStart = "dump", now
	case wire.InstallDependencies:
		t.turn, t.turnStart = "install_dependencies", now
	case wire.Reset:
		t.feed, t.runActive = false, false
		t.abandonPending()
		t.turn, t.turnStart = "reset", now
	case wire.Shutdown:
		t.feed, t.runActive = false, false
		t.abandonPending()
		t.turn = ""
	case wire.ResumeCall:
		t.closeSuspension(extOutcome(r.Result))
	case wire.ResumeNameLookup:
		t.closeSuspension(lookupOutcome(r))
	case wire.ResumeFutures:
		outcome := "resolved"
		if p := t.pending; p != nil && p.kind == "function" && p.eager && len(r.Results) == 1 && r.Results[0].CallID == p.callID {
			outcome = extOutcome(r.Results[0].Result)
		}
		t.closeSuspension(outcome)
	case wire.AbortFeed:
		t.closeSuspension("aborted")
	}
}

// Received records a decoded event.
func (t *TurnMetrics) Received(ev *wire.Event, frameLen int) {
	record(FrameBytes, float64(frameLen), attribute.String("direction", "received"))
	if t.turn == "load" && ev.TotalExecutionMicros > t.reported {
		t.reported = ev.TotalExecutionMicros
	}
	switch ev.Kind {
	case wire.EventPrint:
		var totals [len(printStreams)]int
		for _, seg := range ev.Print {
			i := 2
			switch seg.Stream {
			case 1:
				i = 0
			case 2:
				i = 1
			}
			totals[i] += len(seg.Text)
		}
		for i, n := range totals {
			if n > 0 {
				record(PrintBytes, float64(n), attribute.String("stream", printStreams[i]))
			}
		}
	case wire.EventFunctionCall:
		s := &suspension{kind: "function"}
		if ev.FunctionCall != nil && ev.FunctionCall.AllowEagerAwait {
			s.eager, s.callID = true, ev.FunctionCall.CallID
		}
		t.suspend(s)
	case wire.EventOsCall:
		function := "unknown"
		if ev.OsCall != nil {
			if name, ok := osFunctions[ev.OsCall.Op]; ok {
				function = name
			}
		}
		t.suspend(&suspension{kind: "os", function: function})
	case wire.EventNameLookup:
		t.suspend(&suspension{kind: "name_lookup"})
	case wire.EventResolveFutures:
		t.suspend(&suspension{kind: "futures"})
	case wire.EventComplete:
		t.endRun("complete", ev)
	case wire.EventError:
		if t.turn != "" {
			t.endTurn("error")
		} else {
			t.endRun("error", ev)
		}
	case wire.EventTypingError:
		t.endRun("typing_error", ev)
	case wire.EventDumpResult:
		record(SnapshotBytes, float64(len(ev.State)), attribute.String("op", "dump"))
		t.endTurn("ok")
	case wire.EventOk:
		t.endTurn("ok")
	case wire.EventFatalError:
		t.endTerminal("fatal_error", ev)
	case wire.EventShutdown:
		t.endTerminal("shutdown", ev)
	}
}

// Close releases an open suspension without recording its round trip.
func (t *TurnMetrics) Close() { t.abandonPending() }

func (t *TurnMetrics) suspend(s *suspension) {
	if t.turn == "load" {
		t.endTurn("ok")
	}
	t.runActive = true
	record(Suspensions, 1, attribute.String("kind", s.kind))
	t.abandonPending()
	s.start = time.Now()
	t.pending = s
	record(SuspendedWorkers, 1)
}

func (t *TurnMetrics) closeSuspension(outcome string) {
	s := t.takePending()
	if s == nil {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("kind", s.kind), attribute.String("outcome", outcome)}
	if s.kind == "os" {
		attrs = append(attrs, attribute.String("function", s.function))
	}
	record(ExtCallDuration, time.Since(s.start).Seconds(), attrs...)
}

func (t *TurnMetrics) abandonPending() { t.takePending() }

func (t *TurnMetrics) takePending() *suspension {
	s := t.pending
	t.pending = nil
	if s != nil {
		record(SuspendedWorkers, -1)
	}
	return s
}

func (t *TurnMetrics) endRun(outcome string, ev *wire.Event) {
	t.runActive = false
	t.abandonPending()
	t.endTurn(outcome)
	if t.feed {
		t.feed = false
		record(RunDuration, time.Since(t.feedStart).Seconds(), attribute.String("outcome", outcome))
	}
	var delta uint64
	if total := ev.TotalExecutionMicros; total > t.reported {
		delta = total - t.reported
		t.reported = total
	}
	record(RunExecution, (time.Duration(delta) * time.Microsecond).Seconds())
}

func (t *TurnMetrics) endTerminal(outcome string, ev *wire.Event) {
	if t.runActive {
		t.endRun(outcome, ev)
	} else {
		t.endTurn(outcome)
	}
}

func (t *TurnMetrics) endTurn(outcome string) {
	if t.turn != "" {
		record(TurnDuration, time.Since(t.turnStart).Seconds(), attribute.String("turn", t.turn), attribute.String("outcome", outcome))
		t.turn = ""
	}
}

func extOutcome(r wire.ExtResult) string {
	switch r.Kind {
	case wire.ExtReturn:
		return "value"
	case wire.ExtError:
		return "error"
	case wire.ExtFuture:
		return "future"
	case wire.ExtNotFound:
		return "not_found"
	case wire.ExtNotHandled:
		return "not_handled"
	}
	return "missing"
}

func lookupOutcome(r wire.ResumeNameLookup) string {
	switch r.Kind {
	case wire.LookupValue:
		return "value"
	case wire.LookupUndefined:
		return "undefined"
	case wire.LookupError:
		return "error"
	}
	return "missing"
}
