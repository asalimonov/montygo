package telemetry

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/asalimonov/montygo/internal/wire"
)

type point struct {
	attrs map[string]string
	value float64
	count uint64
	min   float64
	max   float64
}

type metricRig struct {
	reader *sdkmetric.ManualReader
}

func newTurns(t *testing.T) (*TurnMetrics, *metricRig) {
	t.Helper()
	t.Cleanup(Reset)
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	require.True(t, Install(new(byte), Components{Meter: mp.Meter("test")}, false))
	return &TurnMetrics{}, &metricRig{reader: reader}
}

func (r *metricRig) points(t *testing.T, name string) []point {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, r.reader.Collect(context.Background(), &rm))
	var out []point
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range data.DataPoints {
					out = append(out, point{attrs: attrStrings(dp.Attributes), value: float64(dp.Value), count: 1})
				}
			case metricdata.Histogram[float64]:
				for _, dp := range data.DataPoints {
					lo, _ := dp.Min.Value()
					hi, _ := dp.Max.Value()
					out = append(out, point{attrs: attrStrings(dp.Attributes), value: dp.Sum, count: dp.Count, min: lo, max: hi})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return joinAttrs(out[i].attrs) < joinAttrs(out[j].attrs) })
	return out
}

func (r *metricRig) attrs(t *testing.T, name string) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, p := range r.points(t, name) {
		out = append(out, p.attrs)
	}
	return out
}

func (r *metricRig) sum(t *testing.T, name string) float64 {
	t.Helper()
	total := 0.0
	for _, p := range r.points(t, name) {
		total += p.value
	}
	return total
}

func attrStrings(set attribute.Set) map[string]string {
	out := map[string]string{}
	for _, kv := range set.ToSlice() {
		out[string(kv.Key)] = kv.Value.String()
	}
	return out
}

func joinAttrs(attrs map[string]string) string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k+"="+attrs[k])
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func seconds(us int64) float64 { return (time.Duration(us) * time.Microsecond).Seconds() }

func feedRequest() wire.Feed { return wire.Feed{Code: "double(2)", Cwd: "/"} }

func callEvent(name string) *wire.Event {
	return &wire.Event{Kind: wire.EventFunctionCall, FunctionCall: &wire.FunctionCall{FunctionName: name, CallID: 1}}
}

func resumeCall(kind wire.ExtKind) wire.ResumeCall {
	return wire.ResumeCall{CallID: 1, Result: wire.ExtResult{Kind: kind, Value: int64(4), Error: wire.NewException("AttributeError", ""), NotFoundName: "attacker_chosen"}}
}

func completeAt(total uint64) *wire.Event {
	return &wire.Event{Kind: wire.EventComplete, HasValue: true, TotalExecutionMicros: total}
}

func TestTurnsAFeedRecordsItsRunAndItsRoundTrip(t *testing.T) {
	m, rig := newTurns(t)
	m.Sent(feedRequest(), 10)
	m.Received(callEvent("double"), 10)
	m.Sent(resumeCall(wire.ExtReturn), 10)
	m.Received(completeAt(0), 10)

	require.Equal(t, []map[string]string{{"kind": "function"}}, rig.attrs(t, "monty.run.suspensions"))
	require.Equal(t, []map[string]string{{"kind": "function", "outcome": "value"}}, rig.attrs(t, "monty.ext.call.duration"))
	require.Equal(t, []map[string]string{{"outcome": "complete"}}, rig.attrs(t, "monty.run.duration"))
	require.Equal(t, uint64(1), rig.points(t, "monty.run.execution_time")[0].count)
	require.Equal(t, 1.0, rig.sum(t, "monty.run.suspensions"))
	require.Equal(t, []map[string]string{{"direction": "received"}, {"direction": "sent"}}, rig.attrs(t, "monty.wire.frame.bytes"))
}

func TestTurnsSandboxChosenNamesNeverReachAttributes(t *testing.T) {
	m, rig := newTurns(t)
	m.Sent(feedRequest(), 1)
	for _, kind := range []wire.ExtKind{wire.ExtNotFound, wire.ExtError, wire.ExtReturn} {
		m.Received(callEvent("attacker_chosen"), 1)
		m.Sent(resumeCall(kind), 1)
	}
	var outcomes []string
	for _, attrs := range rig.attrs(t, "monty.ext.call.duration") {
		require.Len(t, attrs, 2, attrs)
		require.Equal(t, "function", attrs["kind"])
		outcomes = append(outcomes, attrs["outcome"])
	}
	require.Equal(t, []string{"error", "not_found", "value"}, outcomes)
}

func TestTurnsAPrintEventRecordsOneMeasurementPerStream(t *testing.T) {
	m, rig := newTurns(t)
	segments := make([]wire.PrintSegment, 64)
	for i := range segments {
		segments[i] = wire.PrintSegment{Stream: uint8(1 + i%2), Text: "a"}
	}
	m.Sent(feedRequest(), 1)
	m.Received(&wire.Event{Kind: wire.EventPrint, Print: segments}, 1)
	points := rig.points(t, "monty.print.bytes")
	require.Len(t, points, 2)
	require.Equal(t, map[string]string{"stream": "stderr"}, points[0].attrs)
	require.Equal(t, 32.0, points[0].value)
	require.Equal(t, map[string]string{"stream": "stdout"}, points[1].attrs)
	require.Equal(t, 32.0, points[1].value)
}

func TestTurnsAPrintEventSkipsStreamsWithNoOutput(t *testing.T) {
	m, rig := newTurns(t)
	m.Sent(feedRequest(), 1)
	m.Received(&wire.Event{Kind: wire.EventPrint, Print: []wire.PrintSegment{{Stream: 1, Text: "hello\n"}}}, 1)
	points := rig.points(t, "monty.print.bytes")
	require.Len(t, points, 1)
	require.Equal(t, map[string]string{"stream": "stdout"}, points[0].attrs)
	require.Equal(t, 6.0, points[0].value)
}

func TestTurnsOsCallsCarryTheirProtocolName(t *testing.T) {
	m, rig := newTurns(t)
	m.Sent(feedRequest(), 1)
	m.Received(&wire.Event{Kind: wire.EventOsCall, OsCall: &wire.OsCall{CallID: 1, Op: wire.OpReadText, Path: "/mnt/f.txt"}}, 1)
	m.Sent(resumeCall(wire.ExtReturn), 1)
	require.Equal(t, []map[string]string{{"function": "read_text", "kind": "os", "outcome": "value"}}, rig.attrs(t, "monty.ext.call.duration"))
}

func TestTurnsExecutionTimeIsTheDeltaOfACumulativeClock(t *testing.T) {
	m, rig := newTurns(t)
	for _, total := range []uint64{100, 250} {
		m.Sent(feedRequest(), 1)
		m.Received(completeAt(total), 1)
	}
	points := rig.points(t, "monty.run.execution_time")
	require.Len(t, points, 1)
	require.Equal(t, uint64(2), points[0].count)
	require.InDelta(t, seconds(250), points[0].value, 1e-12)
	require.InDelta(t, seconds(100), points[0].min, 1e-12)
	require.InDelta(t, seconds(150), points[0].max, 1e-12)
}

func TestTurnsAnExceptionEndsTheRunWithoutDescribingItself(t *testing.T) {
	m, rig := newTurns(t)
	m.Sent(feedRequest(), 1)
	m.Received(&wire.Event{Kind: wire.EventError, Exception: wire.NewException("MyCustomError", "")}, 1)
	require.Equal(t, []map[string]string{{"outcome": "error"}}, rig.attrs(t, "monty.run.duration"))
}

func TestTurnsTerminalEventsEndRunsAndHousekeepingTurns(t *testing.T) {
	for kind, outcome := range map[wire.EventKind]string{wire.EventFatalError: "fatal_error", wire.EventShutdown: "shutdown"} {
		m, rig := newTurns(t)
		m.Sent(feedRequest(), 1)
		m.Received(&wire.Event{Kind: kind}, 1)
		require.Equal(t, []map[string]string{{"outcome": outcome}}, rig.attrs(t, "monty.run.duration"))
		require.Equal(t, uint64(1), rig.points(t, "monty.run.execution_time")[0].count)
		Reset()
	}

	m, rig := newTurns(t)
	m.Sent(wire.InstallDependencies{}, 1)
	m.Received(&wire.Event{Kind: wire.EventShutdown}, 1)
	require.Equal(t, []map[string]string{{"outcome": "shutdown", "turn": "install_dependencies"}}, rig.attrs(t, "monty.turn.duration"))
	require.Empty(t, rig.points(t, "monty.run.execution_time"))
}

func TestTurnsALoadRebasesTheExecutionClock(t *testing.T) {
	m, rig := newTurns(t)
	m.Sent(wire.Load{}, 1)
	m.Received(&wire.Event{Kind: wire.EventOk, TotalExecutionMicros: 10_000_000, RestoredScriptName: strPtr("dumped.py")}, 1)
	m.Sent(feedRequest(), 1)
	m.Received(completeAt(10_000_100), 1)
	points := rig.points(t, "monty.run.execution_time")
	require.Equal(t, uint64(1), points[0].count)
	require.InDelta(t, seconds(100), points[0].value, 1e-12)
	require.Equal(t, []map[string]string{{"outcome": "ok", "turn": "load"}}, rig.attrs(t, "monty.turn.duration"))
}

func TestTurnsARestoredSuspensionClosesTheLoadTurn(t *testing.T) {
	m, rig := newTurns(t)
	m.Sent(wire.Load{}, 1)
	m.Received(&wire.Event{Kind: wire.EventNameLookup, NameLookup: &wire.NameLookup{Name: "value"}, TotalExecutionMicros: 10_000_000}, 1)
	require.Equal(t, []map[string]string{{"outcome": "ok", "turn": "load"}}, rig.attrs(t, "monty.turn.duration"))

	m.Sent(wire.ResumeNameLookup{Kind: wire.LookupValue, Value: int64(1)}, 1)
	m.Received(completeAt(10_000_050), 1)
	points := rig.points(t, "monty.run.execution_time")
	require.Equal(t, uint64(1), points[0].count)
	require.InDelta(t, seconds(50), points[0].value, 1e-12)
	require.Empty(t, rig.points(t, "monty.run.duration"))
	require.Equal(t, []map[string]string{{"kind": "name_lookup", "outcome": "value"}}, rig.attrs(t, "monty.ext.call.duration"))
}

func TestTurnsAFailedHousekeepingTurnIsNotARun(t *testing.T) {
	m, rig := newTurns(t)
	m.Sent(wire.InstallDependencies{Requirements: []string{"pydantic"}}, 1)
	m.Received(&wire.Event{Kind: wire.EventError, Exception: wire.NewException("ValueError", "")}, 1)
	require.Equal(t, []map[string]string{{"outcome": "error", "turn": "install_dependencies"}}, rig.attrs(t, "monty.turn.duration"))
	require.Empty(t, rig.points(t, "monty.run.duration"))
	require.Empty(t, rig.points(t, "monty.run.execution_time"))
}

func TestTurnsSuspendedWorkersTracksHostRoundTrips(t *testing.T) {
	m, rig := newTurns(t)
	m.Sent(feedRequest(), 1)
	m.Received(callEvent("double"), 1)
	require.Equal(t, 1.0, rig.sum(t, "monty.pool.workers.suspended"))
	m.Sent(resumeCall(wire.ExtReturn), 1)
	require.Equal(t, 0.0, rig.sum(t, "monty.pool.workers.suspended"))

	m.Received(callEvent("double"), 1)
	m.Sent(wire.AbortFeed{}, 1)
	require.Equal(t, 0.0, rig.sum(t, "monty.pool.workers.suspended"))
	require.Contains(t, rig.attrs(t, "monty.ext.call.duration"), map[string]string{"kind": "function", "outcome": "aborted"})
	m.Received(&wire.Event{Kind: wire.EventError}, 1)

	m.Sent(feedRequest(), 1)
	m.Received(callEvent("double"), 1)
	require.Equal(t, 1.0, rig.sum(t, "monty.pool.workers.suspended"))
	m.Close()
	require.Equal(t, 0.0, rig.sum(t, "monty.pool.workers.suspended"))
}

func TestTurnsEagerFuturesAreMatchedByCallID(t *testing.T) {
	m, rig := newTurns(t)
	m.Sent(feedRequest(), 1)
	m.Received(&wire.Event{Kind: wire.EventFunctionCall, FunctionCall: &wire.FunctionCall{FunctionName: "fetch", CallID: 7, AllowEagerAwait: true}}, 1)
	m.Sent(wire.ResumeFutures{Results: []wire.FutureResult{{CallID: 7, Result: wire.ExtResult{Kind: wire.ExtError}}}}, 1)
	m.Received(&wire.Event{Kind: wire.EventFunctionCall, FunctionCall: &wire.FunctionCall{FunctionName: "fetch", CallID: 8}}, 1)
	m.Sent(wire.ResumeCall{CallID: 8, Result: wire.ExtResult{Kind: wire.ExtFuture}}, 1)
	m.Received(&wire.Event{Kind: wire.EventResolveFutures, PendingCallIDs: []uint32{8}}, 1)
	m.Sent(wire.ResumeFutures{Results: []wire.FutureResult{{CallID: 8, Result: wire.ExtResult{Kind: wire.ExtReturn}}}}, 1)

	require.Equal(t, []map[string]string{
		{"kind": "function", "outcome": "error"},
		{"kind": "function", "outcome": "future"},
		{"kind": "futures", "outcome": "resolved"},
	}, rig.attrs(t, "monty.ext.call.duration"))
}

func TestTurnsADumpReportsItsSizeWithoutEndingTheRun(t *testing.T) {
	m, rig := newTurns(t)
	m.Sent(feedRequest(), 1)
	m.Sent(wire.Dump{}, 1)
	m.Received(&wire.Event{Kind: wire.EventDumpResult, State: make([]byte, 32)}, 1)
	snapshots := rig.points(t, "monty.snapshot.bytes")
	require.Equal(t, uint64(1), snapshots[0].count)
	require.Equal(t, 32.0, snapshots[0].value)
	require.Equal(t, map[string]string{"op": "dump"}, snapshots[0].attrs)
	require.Equal(t, []map[string]string{{"outcome": "ok", "turn": "dump"}}, rig.attrs(t, "monty.turn.duration"))
	require.Empty(t, rig.points(t, "monty.run.duration"))
}
