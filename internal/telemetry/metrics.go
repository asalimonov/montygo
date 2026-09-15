package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Kind is the kind of instrument a metric records into.
type Kind uint8

const (
	Counter Kind = iota
	UpDownCounter
	Histogram
)

// Instrument describes one Monty metric.
type Instrument struct {
	Kind        Kind
	Name        string
	Unit        string
	Description string
}

var (
	LiveWorkers      = &Instrument{UpDownCounter, "monty.pool.workers.live", "{worker}", "Workers the pool is keeping alive: pooled, checked out, or being spawned."}
	IdleWorkers      = &Instrument{UpDownCounter, "monty.pool.workers.idle", "{worker}", "Workers immediately available for checkout."}
	SuspendedWorkers = &Instrument{UpDownCounter, "monty.pool.workers.suspended", "{worker}", "Workers blocked waiting for the host to answer a suspension."}
	CheckoutWait     = &Instrument{Histogram, "monty.pool.checkout.wait", "s", "Time spent acquiring a worker for a checkout."}
	WorkerTerminated = &Instrument{Counter, "monty.pool.worker.terminated", "{worker}", "Workers discarded by the pool, by reason."}
	SessionDuration  = &Instrument{Histogram, "monty.pool.session.duration", "s", "Lifetime of a checked-out session."}
	RunDuration      = &Instrument{Histogram, "monty.run.duration", "s", "Wall time of one feed, including time spent waiting on the host."}
	RunExecution     = &Instrument{Histogram, "monty.run.execution_time", "s", "Sandbox execution time of one feed, excluding host round-trips."}
	TurnDuration     = &Instrument{Histogram, "monty.turn.duration", "s", "Wall time of one non-execution turn, by kind."}
	Suspensions      = &Instrument{Counter, "monty.run.suspensions", "{suspension}", "Suspensions the sandbox raised for the host to answer, by kind."}
	ExtCallDuration  = &Instrument{Histogram, "monty.ext.call.duration", "s", "Time the host took to answer a suspension, by kind."}
	SnapshotBytes    = &Instrument{Histogram, "monty.snapshot.bytes", "By", "Size of a session dump."}
	PrintBytes       = &Instrument{Counter, "monty.print.bytes", "By", "Bytes the sandbox printed, by stream."}
	FrameBytes       = &Instrument{Histogram, "monty.wire.frame.bytes", "By", "Size of one protocol frame, by direction."}
)

type handle struct {
	counter   metric.Int64Counter
	upDown    metric.Int64UpDownCounter
	histogram metric.Float64Histogram
}

// Record adds one measurement: an increment for counters, a sample for histograms.
func (r *Recorder) Record(in *Instrument, v float64, attrs ...attribute.KeyValue) {
	if !r.Metering() {
		return
	}
	guard(&r.metricsOff, func() error {
		h, err := r.instrument(in)
		if err != nil {
			return err
		}
		opt := metric.WithAttributes(attrs...)
		ctx := context.Background()
		switch in.Kind {
		case Counter:
			h.counter.Add(ctx, int64(v), opt)
		case UpDownCounter:
			h.upDown.Add(ctx, int64(v), opt)
		case Histogram:
			h.histogram.Record(ctx, v, opt)
		}
		return nil
	})
}

func (r *Recorder) instrument(in *Instrument) (*handle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.instruments[in.Name]; ok {
		return h, nil
	}
	h := &handle{}
	var err error
	switch in.Kind {
	case Counter:
		h.counter, err = r.c.Meter.Int64Counter(in.Name, metric.WithUnit(in.Unit), metric.WithDescription(in.Description))
	case UpDownCounter:
		h.upDown, err = r.c.Meter.Int64UpDownCounter(in.Name, metric.WithUnit(in.Unit), metric.WithDescription(in.Description))
	case Histogram:
		h.histogram, err = r.c.Meter.Float64Histogram(in.Name, metric.WithUnit(in.Unit), metric.WithDescription(in.Description))
	}
	if err != nil {
		return nil, err
	}
	r.instruments[in.Name] = h
	return h, nil
}

func record(in *Instrument, v float64, attrs ...attribute.KeyValue) {
	Current().Record(in, v, attrs...)
}

// PoolMetrics records pool-level measurements into the recorder installed
// when each measurement is taken.
type PoolMetrics struct{}

func (PoolMetrics) WorkersLive(delta int64) { record(LiveWorkers, float64(delta)) }

func (PoolMetrics) WorkersIdle(delta int64) { record(IdleWorkers, float64(delta)) }

func (PoolMetrics) CheckoutWait(d time.Duration, outcome string) {
	record(CheckoutWait, d.Seconds(), attribute.String("outcome", outcome))
}

func (PoolMetrics) WorkerTerminated(reason string) {
	record(WorkerTerminated, 1, attribute.String("reason", reason))
}

func (PoolMetrics) SessionDuration(d time.Duration, outcome string) {
	record(SessionDuration, d.Seconds(), attribute.String("outcome", outcome))
}
