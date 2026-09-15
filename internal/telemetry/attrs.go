package telemetry

import (
	"math"
	"strconv"

	"go.opentelemetry.io/otel/attribute"

	"github.com/asalimonov/montygo/internal/wire"
)

const (
	cutKey  = "length_limit_exceeded"
	missing = "<missing>"
)

func flagCut(attrs []attribute.KeyValue, cut bool) []attribute.KeyValue {
	if cut {
		return append(attrs, attribute.Bool(cutKey, true))
	}
	return attrs
}

func saturate(u uint64) int64 {
	if u > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(u)
}

func eventBudget(ev *wire.Event) []attribute.KeyValue {
	attrs := []attribute.KeyValue{attribute.Int64("total_execution_micros", saturate(ev.TotalExecutionMicros))}
	if ev.MaxDurationMicros != nil {
		attrs = append(attrs, attribute.Int64("max_duration_micros", saturate(*ev.MaxDurationMicros)))
	}
	return attrs
}

func configureAttributes(c wire.Configure, pid int, hasPID bool) []attribute.KeyValue {
	name, cut := Truncate(c.ScriptName)
	version, versionCut := Truncate(c.MontyVersion)
	cut = cut || versionCut
	attrs := []attribute.KeyValue{
		attribute.String("script_name", name),
		attribute.String("monty_version", version),
		attribute.Bool("type_check", c.TypeCheck),
	}
	if c.TypeCheckStubs != nil {
		stubs, stubsCut := Truncate(*c.TypeCheckStubs)
		attrs = append(attrs, attribute.String("type_check_stubs", stubs))
		cut = cut || stubsCut
	}
	attrs = flagCut(attrs, cut)
	if c.AssertMessageAnnotations != nil {
		attrs = append(attrs, attribute.Int64("assert_message_annotations", int64(*c.AssertMessageAnnotations)))
	}
	if l := c.Limits; l != nil {
		for _, limit := range []struct {
			key   string
			value *uint64
		}{
			{"max_duration_micros", l.MaxDurationMicros},
			{"max_memory_bytes", l.MaxMemoryBytes},
			{"gc_interval", l.GCInterval},
			{"max_recursion_depth", l.MaxRecursionDepth},
			{"max_suspensions", l.MaxSuspensions},
		} {
			if limit.value != nil {
				attrs = append(attrs, attribute.Int64(limit.key, saturate(*limit.value)))
			}
		}
	}
	if hasPID {
		attrs = append(attrs, attribute.Int64("worker_pid", int64(pid)))
	}
	return attrs
}

func printStream(stream uint8) string {
	switch stream {
	case 1:
		return "stdout"
	case 2:
		return "stderr"
	}
	return "unspecified"
}

var osFunctions = map[wire.OsOp]string{
	wire.OpExists: "exists", wire.OpIsFile: "is_file", wire.OpIsDir: "is_dir", wire.OpIsSymlink: "is_symlink",
	wire.OpReadText: "read_text", wire.OpReadBytes: "read_bytes", wire.OpStat: "stat", wire.OpIterdir: "iterdir",
	wire.OpResolve: "resolve", wire.OpAbsolute: "absolute", wire.OpUnlink: "unlink", wire.OpRmdir: "rmdir",
	wire.OpWriteText: "write_text", wire.OpAppendText: "append_text", wire.OpWriteBytes: "write_bytes",
	wire.OpAppendBytes: "append_bytes", wire.OpOpen: "open", wire.OpMkdir: "mkdir", wire.OpRename: "rename",
	wire.OpGetenv: "getenv", wire.OpGetEnviron: "get_environ", wire.OpDateToday: "date_today",
	wire.OpDateTimeNow: "date_time_now",
}

func osArguments(call *wire.OsCall) ([]attribute.KeyValue, bool) {
	var attrs []attribute.KeyValue
	cut := false
	str := func(key, s string) {
		t, c := Truncate(s)
		cut = cut || c
		attrs = append(attrs, attribute.String(key, t))
	}
	switch call.Op {
	case wire.OpExists, wire.OpIsFile, wire.OpIsDir, wire.OpIsSymlink, wire.OpReadText, wire.OpReadBytes,
		wire.OpStat, wire.OpIterdir, wire.OpResolve, wire.OpAbsolute, wire.OpUnlink, wire.OpRmdir:
		str("args.path", call.Path)
	case wire.OpWriteText, wire.OpAppendText:
		str("args.path", call.Path)
		str("args.data", call.Text)
	case wire.OpWriteBytes, wire.OpAppendBytes:
		str("args.path", call.Path)
		data, c := BytesText(call.Data)
		cut = cut || c
		attrs = append(attrs, attribute.String("args.data", data))
	case wire.OpOpen:
		str("args.path", call.Path)
		str("args.mode", call.Mode)
	case wire.OpMkdir:
		str("args.path", call.Path)
		attrs = append(attrs, attribute.Bool("args.parents", call.Parents), attribute.Bool("args.exist_ok", call.ExistOK))
	case wire.OpRename:
		str("args.src", call.Path)
		str("args.dst", call.Dst)
	case wire.OpGetenv:
		str("args.key", call.Key)
		if call.Default != nil {
			v, c := AttrValue(call.Default)
			cut = cut || c
			attrs = append(attrs, attribute.String("args.default", v.String()))
		}
	case wire.OpDateTimeNow:
		if call.HasTZ {
			attrs = append(attrs, attribute.Int64("args.tz_offset_seconds", int64(call.TZ.OffsetSeconds)))
			if call.TZ.Name != nil {
				str("args.tz_name", *call.TZ.Name)
			}
		}
	}
	return attrs, cut
}

func renderExtResult(r wire.ExtResult) (attribute.Value, bool) {
	switch r.Kind {
	case wire.ExtReturn:
		return AttrValue(r.Value)
	case wire.ExtError:
		return renderRaised(r.Error)
	case wire.ExtFuture:
		return attribute.StringValue("future " + strconv.FormatUint(uint64(r.FutureCallID), 10)), false
	case wire.ExtNotFound:
		var t cappedText
		t.write("not found: " + r.NotFoundName)
		return attribute.StringValue(t.String()), t.cut
	case wire.ExtNotHandled:
		return attribute.StringValue("not handled"), false
	}
	return attribute.StringValue(missing), false
}

func renderNameLookup(r wire.ResumeNameLookup) (attribute.Value, bool) {
	switch r.Kind {
	case wire.LookupValue:
		return AttrValue(r.Value)
	case wire.LookupUndefined:
		return attribute.StringValue("undefined"), false
	case wire.LookupError:
		return renderRaised(r.Error)
	}
	return attribute.StringValue(missing), false
}

func renderRaised(e *wire.Exception) (attribute.Value, bool) {
	if e == nil {
		return attribute.StringValue(missing), false
	}
	var t cappedText
	t.write("raise " + e.ExcType)
	if e.Message != nil {
		t.write(": " + *e.Message)
	}
	return attribute.StringValue(t.String()), t.cut
}

func renderFutureResults(results []wire.FutureResult) (string, bool) {
	var t cappedText
	resultCut := false
	for i, r := range results {
		if t.cut {
			break
		}
		if i > 0 {
			t.write(", ")
		}
		v, c := renderExtResult(r.Result)
		resultCut = resultCut || c
		t.write(strconv.FormatUint(uint64(r.CallID), 10) + ": " + v.String())
	}
	return t.String(), resultCut || t.cut
}

func renderTraceback(frames []wire.Frame) (string, bool) {
	var t cappedText
	for i, f := range frames {
		if i > 0 {
			t.write("\n")
		}
		name := "<module>"
		if f.FrameName != nil {
			name = *f.FrameName
		}
		t.write(f.Filename + ":" + strconv.FormatUint(uint64(f.Start.Line), 10) + " in " + name)
	}
	return t.String(), t.cut
}

func excDataAttributes(d *wire.ExcData) ([]attribute.KeyValue, bool) {
	switch {
	case d.Unicode != nil:
		u := d.Unicode
		object, objectCut := missing, false
		switch {
		case u.ObjectStr != nil:
			object, objectCut = Truncate(*u.ObjectStr)
		case u.ObjectBytes != nil:
			object, objectCut = BytesText(u.ObjectBytes)
		}
		encoding, encodingCut := Truncate(u.Encoding)
		reason, reasonCut := Truncate(u.Reason)
		return []attribute.KeyValue{
			attribute.String("exc_data.encoding", encoding),
			attribute.String("exc_data.object", object),
			attribute.Int64("exc_data.start", saturate(u.Start)),
			attribute.Int64("exc_data.end", saturate(u.End)),
			attribute.String("exc_data.reason", reason),
		}, objectCut || encodingCut || reasonCut
	case d.JSON != nil:
		j := d.JSON
		msg, cut := Truncate(j.Msg)
		attrs := []attribute.KeyValue{attribute.String("exc_data.msg", msg)}
		if j.Doc != nil {
			doc, docCut := Truncate(*j.Doc)
			attrs = append(attrs, attribute.String("exc_data.doc", doc))
			cut = cut || docCut
		}
		return append(attrs,
			attribute.Int64("exc_data.pos", saturate(j.Pos)),
			attribute.Int64("exc_data.lineno", saturate(j.Lineno)),
			attribute.Int64("exc_data.colno", saturate(j.Colno)),
		), cut
	}
	return nil, false
}
