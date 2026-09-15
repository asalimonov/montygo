package telemetry

import (
	"context"
	"math/rand/v2"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Span is a Monty span: an OpenTelemetry span, or, while only logs are
// recorded, a span context that keeps log records correlated.
type Span struct {
	rec   *Recorder
	span  trace.Span
	sc    trace.SpanContext
	owner any
}

// StartSpan starts a span under the span in parent, tagged with owner. It
// returns nil when neither spans nor logs are recorded.
func (r *Recorder) StartSpan(parent context.Context, name string, attrs []attribute.KeyValue, owner any) *Span {
	if r.Tracing() {
		var span trace.Span
		if guard(&r.tracesOff, func() error {
			_, span = r.c.Tracer.Start(parent, name, trace.WithAttributes(attrs...))
			if span == nil {
				return errNilSpan
			}
			return nil
		}) {
			return &Span{rec: r, span: span, owner: owner}
		}
	}
	if r.Logging() {
		return &Span{rec: r, sc: newSpanContext(parent), owner: owner}
	}
	return nil
}

func newSpanContext(parent context.Context) trace.SpanContext {
	cfg := trace.SpanContextConfig{TraceFlags: trace.FlagsSampled}
	if p := trace.SpanContextFromContext(parent); p.IsValid() {
		cfg.TraceID, cfg.TraceFlags, cfg.TraceState = p.TraceID(), p.TraceFlags(), p.TraceState()
	} else {
		randomID(cfg.TraceID[:])
	}
	randomID(cfg.SpanID[:])
	return trace.NewSpanContext(cfg)
}

func randomID(b []byte) {
	for isZero(b) {
		for i := range b {
			b[i] = byte(rand.Uint32())
		}
	}
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// Context returns ctx carrying the span; a nil span returns ctx.
func (s *Span) Context(ctx context.Context) context.Context {
	switch {
	case s == nil:
		return ctx
	case s.span != nil:
		return trace.ContextWithSpan(ctx, s.span)
	}
	return trace.ContextWithSpanContext(ctx, s.sc)
}

// OwnedBy reports whether the span was started for owner.
func (s *Span) OwnedBy(owner any) bool { return s != nil && s.owner == owner }

// SetAttributes adds attributes while the span's recorder still traces.
func (s *Span) SetAttributes(attrs ...attribute.KeyValue) {
	if s == nil || s.span == nil || len(attrs) == 0 || !s.rec.Tracing() {
		return
	}
	guard(&s.rec.tracesOff, func() error {
		s.span.SetAttributes(attrs...)
		return nil
	})
}

// End ends the span while its recorder is installed and still traces.
func (s *Span) End() {
	if s == nil || s.span == nil || !s.rec.Tracing() {
		return
	}
	guard(&s.rec.tracesOff, func() error {
		s.span.End()
		return nil
	})
}
