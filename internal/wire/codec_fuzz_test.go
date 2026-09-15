package wire

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/asalimonov/montygo/internal/value"
	pb "github.com/asalimonov/montygo/montypb"
)

const maxFuzzInput = 1 << 20

// wireErrs are the errors protowire reports for malformed wire data.
var wireErrs = func() []error {
	var errs []error
	for n := -1; n >= -7; n-- {
		errs = append(errs, protowire.ParseError(n))
	}
	return errs
}()

func isWireError(err error) bool {
	for _, w := range wireErrs {
		if errors.Is(err, w) {
			return true
		}
	}
	return false
}

// handLaxness walks a message the hand decoder accepted and reports whether it
// carries one of the three things the hand decoder does not check but protobuf
// rejects: a field number above protowire.MaxValidNumber, invalid UTF-8 in a
// string field, or payload bytes in a field whose message type has no fields
// (Unit, Ok), which the hand decoder consumes without parsing.
func handLaxness(schema protoSchema, msg []byte, message string) bool {
	fields := schema[message]
	if len(fields) == 0 {
		return len(msg) > 0
	}
	for len(msg) > 0 {
		num, typ, n := protowire.ConsumeTag(msg)
		if n < 0 {
			return false
		}
		if num > protowire.MaxValidNumber {
			return true
		}
		msg = msg[n:]
		ftype, known := fields[int32(num)]
		if !known || typ != protowire.BytesType {
			if n = protowire.ConsumeFieldValue(num, typ, msg); n < 0 {
				return false
			}
			msg = msg[n:]
			continue
		}
		v, n := protowire.ConsumeBytes(msg)
		if n < 0 {
			return false
		}
		msg = msg[n:]
		if ftype == "string" && !utf8.Valid(v) {
			return true
		}
		if child := schema.resolve(message, ftype); child != "" && handLaxness(schema, v, child) {
			return true
		}
	}
	return false
}

func seedEvents() []*pb.ChildEvent {
	exc := &pb.RaisedException{ExcType: "ZeroDivisionError", Message: ptr("division by zero"), Traceback: []*pb.StackFrame{{
		Filename: "<python-input-1>", Start: &pb.CodeLoc{Line: 1, Column: 1}, End: &pb.CodeLoc{Line: 1, Column: 6}, PreviewLine: ptr("1 / 0"), FrameName: ptr("f"), HideCaret: true,
	}}}
	unicodeExc := &pb.RaisedException{ExcType: "UnicodeDecodeError", Message: ptr("bad"), Data: &pb.ExcData{Kind: &pb.ExcData_Unicode{Unicode: &pb.UnicodeErrorData{
		Encoding: "utf-8", Object: &pb.UnicodeErrorData_ObjectBytes{ObjectBytes: []byte{0xff}}, Start: 0, End: 1, Reason: "invalid start byte",
	}}}}
	jsonExc := &pb.RaisedException{ExcType: "json.JSONDecodeError", Message: ptr("bad"), Data: &pb.ExcData{Kind: &pb.ExcData_Json{Json: &pb.JsonErrorData{
		Msg: "Expecting value", Doc: ptr("{"), Pos: 1, Lineno: 1, Colno: 2,
	}}}}
	events := []*pb.ChildEvent{
		{Kind: &pb.ChildEvent_Print{Print: &pb.Print{Segments: []*pb.PrintSegment{{Stream: pb.PrintStream_PRINT_STREAM_STDOUT, Text: "a"}, {Stream: pb.PrintStream_PRINT_STREAM_STDERR, Text: "b"}}}}},
		{Kind: &pb.ChildEvent_FunctionCall{FunctionCall: &pb.FunctionCall{
			FunctionName: "fetch", Args: []*pb.MontyObject{pbInt(1), pbStr("x")}, Kwargs: []*pb.Pair{{Key: pbStr("k"), Value: pbStr("v")}},
			CallId: 7, ObjectId: &pb.Uuid{Data: testUUIDBytes()}, AllowEagerAwait: true,
		}}, TotalExecutionMicros: 99, MaxDurationMicros: ptr(uint64(5)), MaxSuspensions: ptr(uint64(10)), RestoredScriptName: ptr("s.py")},
		{Kind: &pb.ChildEvent_ResolveFutures{ResolveFutures: &pb.ResolveFutures{PendingCallIds: []uint32{1, 2, 300}}}},
		{Kind: &pb.ChildEvent_NameLookup{NameLookup: &pb.NameLookup{Name: "x", ObjectId: &pb.Uuid{Data: testUUIDBytes()}}}},
		{Kind: &pb.ChildEvent_Error{Error: &pb.Error{Exception: exc}}, TotalExecutionMicros: 3},
		{Kind: &pb.ChildEvent_Error{Error: &pb.Error{Exception: unicodeExc}}},
		{Kind: &pb.ChildEvent_Error{Error: &pb.Error{Exception: jsonExc}}},
		{Kind: &pb.ChildEvent_TypingError{TypingError: &pb.TypingError{Diagnostics: "error: x"}}},
		{Kind: &pb.ChildEvent_DumpResult{DumpResult: &pb.DumpResult{State: []byte{1, 2, 3}}}},
		{Kind: &pb.ChildEvent_Ok{Ok: &pb.Ok{}}},
		{Kind: &pb.ChildEvent_FatalError{FatalError: &pb.FatalError{Message: "boom"}}},
		{Kind: &pb.ChildEvent_Shutdown{Shutdown: &pb.ShutdownDump{Dump: []byte{9}}}},
		{Kind: &pb.ChildEvent_Complete{Complete: &pb.Complete{Value: &pb.MontyObject{Kind: &pb.MontyObject_Repr{Repr: "<C object at 0x5>"}}}}},
		{Kind: &pb.ChildEvent_Complete{Complete: &pb.Complete{Value: &pb.MontyObject{Kind: &pb.MontyObject_Cycle{Cycle: &pb.Cycle{Identity: 9, Placeholder: "[...]"}}}}}},
	}
	for _, c := range codecCases() {
		events = append(events, &pb.ChildEvent{Kind: &pb.ChildEvent_Complete{Complete: &pb.Complete{Value: c.pbV}}, TotalExecutionMicros: 1})
	}
	for _, oc := range []*pb.OsCall{
		{CallId: 1, Call: &pb.OsCall_ReadText{ReadText: "/a"}},
		{Call: &pb.OsCall_Exists{Exists: "/a"}},
		{Call: &pb.OsCall_WriteBytes{WriteBytes: &pb.OsCall_BytesWrite{Path: "/b", Data: []byte("x")}}},
		{Call: &pb.OsCall_AppendText{AppendText: &pb.OsCall_TextWrite{Path: "/b", Data: "x"}}},
		{Call: &pb.OsCall_Open_{Open: &pb.OsCall_Open{Path: "/f", Mode: "rb"}}},
		{Call: &pb.OsCall_Mkdir_{Mkdir: &pb.OsCall_Mkdir{Path: "/d", Parents: true, ExistOk: true}}},
		{Call: &pb.OsCall_Rename_{Rename: &pb.OsCall_Rename{Src: "/s", Dst: "/t"}}},
		{Call: &pb.OsCall_Getenv_{Getenv: &pb.OsCall_Getenv{Key: "HOME", Default: pbStr("d")}}},
		{Call: &pb.OsCall_GetEnviron{GetEnviron: &pb.Unit{}}},
		{Call: &pb.OsCall_DateToday{DateToday: &pb.Unit{}}},
		{Call: &pb.OsCall_DateTimeNow_{DateTimeNow: &pb.OsCall_DateTimeNow{Tz: &pb.TimeZone{OffsetSeconds: 60, Name: ptr("X")}}}},
	} {
		events = append(events, &pb.ChildEvent{Kind: &pb.ChildEvent_OsCall{OsCall: oc}})
	}
	return events
}

func seedRequests() []*pb.ParentRequest {
	exc := &pb.RaisedException{ExcType: "ValueError", Message: ptr("nope")}
	inputs := []*pb.NamedValue{}
	for _, c := range codecCases() {
		inputs = append(inputs, &pb.NamedValue{Name: c.name, Value: c.pbV})
	}
	return []*pb.ParentRequest{
		{Kind: &pb.ParentRequest_Configure{Configure: &pb.Configure{
			ScriptName: "main.py", MontyVersion: "0.0.23", ProtocolVersion: 3, TypeCheck: true, TypeCheckStubs: ptr("x"),
			AssertMessageAnnotations: ptr(uint32(0)), TypeCheckFormat: pb.TypeCheckFormat_TYPE_CHECK_FORMAT_JSON, TypeCheckColor: true,
			PrintFlushIntervalMs: ptr(uint32(0)), Limits: &pb.ResourceLimits{MaxDurationMicros: ptr(uint64(5)), MaxMemoryBytes: ptr(uint64(6)), GcInterval: ptr(uint64(7)), MaxRecursionDepth: ptr(uint64(1000)), MaxSuspensions: ptr(uint64(0))},
		}}, TraceParent: ptr("00-abc")},
		{Kind: &pb.ParentRequest_InstallDependencies{InstallDependencies: &pb.InstallDependencies{Requirements: []string{"a", "b"}}}},
		{Kind: &pb.ParentRequest_Feed{Feed: &pb.Feed{Code: "x", Inputs: inputs, SkipTypeCheck: true, Cwd: "/data"}}},
		{Kind: &pb.ParentRequest_ResumeCall{ResumeCall: &pb.ResumeCall{CallId: 3, Result: &pb.ExtFunctionResult{Kind: &pb.ExtFunctionResult_Error{Error: exc}}}}},
		{Kind: &pb.ParentRequest_ResumeCall{ResumeCall: &pb.ResumeCall{CallId: 4, Result: &pb.ExtFunctionResult{Kind: &pb.ExtFunctionResult_ReturnValue{ReturnValue: pbStr("v")}}}}},
		{Kind: &pb.ParentRequest_ResumeCall{ResumeCall: &pb.ResumeCall{Result: &pb.ExtFunctionResult{Kind: &pb.ExtFunctionResult_NotHandled{NotHandled: &pb.Unit{}}}}}},
		{Kind: &pb.ParentRequest_ResumeFutures{ResumeFutures: &pb.ResumeFutures{Results: []*pb.FutureResult{
			{CallId: 0, Result: &pb.ExtFunctionResult{Kind: &pb.ExtFunctionResult_Future{Future: 0}}},
			{CallId: 1, Result: &pb.ExtFunctionResult{Kind: &pb.ExtFunctionResult_NotFound{NotFound: "f"}}},
		}}}},
		{Kind: &pb.ParentRequest_ResumeNameLookup{ResumeNameLookup: &pb.ResumeNameLookup{Kind: &pb.ResumeNameLookup_Value{Value: pbInt(1)}}}},
		{Kind: &pb.ParentRequest_ResumeNameLookup{ResumeNameLookup: &pb.ResumeNameLookup{Kind: &pb.ResumeNameLookup_Undefined{Undefined: &pb.Unit{}}}}},
		{Kind: &pb.ParentRequest_ResumeNameLookup{ResumeNameLookup: &pb.ResumeNameLookup{Kind: &pb.ResumeNameLookup_Error{Error: exc}}}},
		{Kind: &pb.ParentRequest_Dump{Dump: &pb.Dump{}}},
		{Kind: &pb.ParentRequest_Load{Load: &pb.Load{State: []byte("abc")}}},
		{Kind: &pb.ParentRequest_Reset_{Reset_: &pb.Reset{}}},
		{Kind: &pb.ParentRequest_Shutdown{Shutdown: &pb.Shutdown{}}},
		{Kind: &pb.ParentRequest_AbortFeed{AbortFeed: &pb.AbortFeed{Exception: exc}}},
	}
}

// FuzzDecodeEvent checks the hand event decoder against the generated one.
//
// Invariants:
//   - The hand decoder never panics.
//   - On input the generated decoder accepts, the hand decoder accepts too or
//     rejects for a reason of its own (wire-type strictness, semantic validation,
//     the decode budget, the nesting limit). It never reports a wire parse error.
//   - On input the generated decoder rejects, the hand decoder rejects too, except
//     for the three things it does not inspect (see handLaxness): field numbers
//     above the protobuf maximum, UTF-8 validity of string fields, and the payload
//     bytes of Unit-typed and Ok arms.
func FuzzDecodeEvent(f *testing.F) {
	schema := loadProtoSchema(f)
	for _, ev := range seedEvents() {
		raw, err := proto.Marshal(ev)
		require.NoError(f, err)
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFuzzInput {
			t.Skip()
		}
		_, handErr := DecodeEvent(data)
		pbErr := proto.Unmarshal(data, &pb.ChildEvent{})
		switch {
		case pbErr == nil && handErr != nil:
			if isWireError(handErr) {
				t.Fatalf("hand decoder reports a wire parse error on data protobuf accepts: %v", handErr)
			}
		case pbErr != nil && handErr == nil:
			if !handLaxness(schema, data, "ChildEvent") {
				t.Fatalf("hand decoder accepts data protobuf rejects: %v", pbErr)
			}
		}
	})
}

func handValue(t *testing.T, obj *pb.MontyObject) (any, bool) {
	t.Helper()
	raw, err := proto.Marshal(obj)
	require.NoError(t, err)
	v, err := DecodeValue(raw, NewBudget(DefaultMaxDecodeBytes))
	if err != nil {
		return nil, false
	}
	return v, true
}

// handException decodes an exception with the hand decoder. protobuf keeps a
// field with a mismatched wire type as an unknown field and re-emits it, which
// the stricter hand decoder rejects; that reports false like any other rejection.
func handException(t *testing.T, e *pb.RaisedException) (*Exception, bool) {
	t.Helper()
	raw, err := proto.Marshal(e)
	require.NoError(t, err)
	exc, err := decodeException(raw)
	if err != nil {
		return nil, false
	}
	return exc, true
}

func toExtResult(t *testing.T, r *pb.ExtFunctionResult) (ExtResult, bool) {
	t.Helper()
	switch k := r.GetKind().(type) {
	case *pb.ExtFunctionResult_ReturnValue:
		v, ok := handValue(t, k.ReturnValue)
		return ExtResult{Kind: ExtReturn, Value: v}, ok
	case *pb.ExtFunctionResult_Error:
		exc, ok := handException(t, k.Error)
		return ExtResult{Kind: ExtError, Error: exc}, ok
	case *pb.ExtFunctionResult_Future:
		return ExtResult{Kind: ExtFuture, FutureCallID: k.Future}, true
	case *pb.ExtFunctionResult_NotFound:
		return ExtResult{Kind: ExtNotFound, NotFoundName: k.NotFound}, true
	case *pb.ExtFunctionResult_NotHandled:
		return ExtResult{Kind: ExtNotHandled}, true
	}
	return ExtResult{}, false
}

// toRequest rebuilds a hand Request from a generated ParentRequest, decoding
// every embedded value with the hand value decoder. It reports false when the
// message has no arm or carries a value the hand decoder rejects.
func toRequest(t *testing.T, in *pb.ParentRequest) (Request, bool) {
	t.Helper()
	switch k := in.Kind.(type) {
	case *pb.ParentRequest_Configure:
		c := k.Configure
		req := Configure{
			ScriptName: c.ScriptName, TypeCheck: c.TypeCheck, TypeCheckStubs: c.TypeCheckStubs, MontyVersion: c.MontyVersion,
			AssertMessageAnnotations: c.AssertMessageAnnotations, TypeCheckFormat: int32(c.TypeCheckFormat), TypeCheckColor: c.TypeCheckColor,
			ProtocolVersion: c.ProtocolVersion, PrintFlushIntervalMs: c.PrintFlushIntervalMs,
		}
		if l := c.Limits; l != nil {
			req.Limits = &Limits{MaxDurationMicros: l.MaxDurationMicros, MaxMemoryBytes: l.MaxMemoryBytes, GCInterval: l.GcInterval, MaxRecursionDepth: l.MaxRecursionDepth, MaxSuspensions: l.MaxSuspensions}
		}
		return req, true
	case *pb.ParentRequest_InstallDependencies:
		return InstallDependencies{Requirements: k.InstallDependencies.Requirements}, true
	case *pb.ParentRequest_Feed:
		fd := Feed{Code: k.Feed.Code, SkipTypeCheck: k.Feed.SkipTypeCheck, Cwd: k.Feed.Cwd}
		for _, in := range k.Feed.Inputs {
			v, ok := handValue(t, in.Value)
			if !ok {
				return nil, false
			}
			fd.Inputs = append(fd.Inputs, NamedValue{Name: in.Name, Value: v})
		}
		return fd, true
	case *pb.ParentRequest_ResumeCall:
		res, ok := toExtResult(t, k.ResumeCall.Result)
		return ResumeCall{CallID: k.ResumeCall.CallId, Result: res}, ok
	case *pb.ParentRequest_ResumeNameLookup:
		switch a := k.ResumeNameLookup.GetKind().(type) {
		case *pb.ResumeNameLookup_Value:
			v, ok := handValue(t, a.Value)
			return ResumeNameLookup{Kind: LookupValue, Value: v}, ok
		case *pb.ResumeNameLookup_Undefined:
			return ResumeNameLookup{Kind: LookupUndefined}, true
		case *pb.ResumeNameLookup_Error:
			exc, ok := handException(t, a.Error)
			return ResumeNameLookup{Kind: LookupError, Error: exc}, ok
		}
		return nil, false
	case *pb.ParentRequest_ResumeFutures:
		rf := ResumeFutures{}
		for _, fr := range k.ResumeFutures.Results {
			res, ok := toExtResult(t, fr.Result)
			if !ok {
				return nil, false
			}
			rf.Results = append(rf.Results, FutureResult{CallID: fr.CallId, Result: res})
		}
		return rf, true
	case *pb.ParentRequest_Dump:
		return Dump{}, true
	case *pb.ParentRequest_Load:
		return Load{State: k.Load.State}, true
	case *pb.ParentRequest_Reset_:
		return Reset{}, true
	case *pb.ParentRequest_Shutdown:
		return Shutdown{}, true
	case *pb.ParentRequest_AbortFeed:
		exc, ok := handException(t, k.AbortFeed.Exception)
		return AbortFeed{Exception: exc}, ok
	}
	return nil, false
}

func containsCycle(v any) bool {
	switch x := v.(type) {
	case value.Cycle:
		return true
	case []any:
		return seqContainsCycle(x)
	case value.Tuple:
		return seqContainsCycle(x)
	case *value.Set:
		return seqContainsCycle(x.Items())
	case *value.FrozenSet:
		return seqContainsCycle(x.Items())
	case value.NamedTuple:
		return seqContainsCycle(x.Values)
	case *value.Dict:
		return pairsContainCycle(x.Pairs())
	case value.Type:
		return pairsContainCycle(x.Attrs.Pairs())
	case value.Instance:
		return pairsContainCycle(x.Type.Attrs.Pairs()) || pairsContainCycle(x.Attrs.Pairs())
	}
	return false
}

func seqContainsCycle(items []any) bool {
	for _, it := range items {
		if containsCycle(it) {
			return true
		}
	}
	return false
}

func pairsContainCycle(pairs []value.Pair) bool {
	for _, p := range pairs {
		if containsCycle(p.Key) || containsCycle(p.Value) {
			return true
		}
	}
	return false
}

// FuzzDecodeRequest drives the request path. The hand codec only encodes
// requests, so the generated decoder parses the input, the hand value decoder
// decodes every embedded value, and the hand encoder re-emits the request.
//
// Invariants:
//   - Neither the hand value decoder nor the hand request encoder panics.
//   - Every value the hand decoder produces encodes, except the output-only cycle
//     marker, which the encoder refuses by design.
//   - The hand encoding parses with the generated decoder, keeps the request arm
//     and the trace parent, and the value codec is idempotent:
//     encode(decode(encode(v))) == encode(v).
func FuzzDecodeRequest(f *testing.F) {
	for _, req := range seedRequests() {
		raw, err := proto.Marshal(req)
		require.NoError(f, err)
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFuzzInput {
			t.Skip()
		}
		in := &pb.ParentRequest{}
		if err := proto.Unmarshal(data, in); err != nil {
			return
		}
		req, ok := toRequest(t, in)
		if !ok {
			return
		}
		out, err := EncodeRequest(req, in.GetTraceParent())
		if err != nil {
			for _, v := range RequestValues(req) {
				if containsCycle(v) {
					return
				}
			}
			t.Fatalf("hand encoder rejects a decoded %s: %v", req.Name(), err)
		}
		got := &pb.ParentRequest{}
		require.NoError(t, proto.Unmarshal(out, got), "hand encoding does not parse")
		require.Equal(t, reflect.TypeOf(in.Kind), reflect.TypeOf(got.Kind), "request arm changed")
		require.Equal(t, in.GetTraceParent(), got.GetTraceParent())
		for _, v := range RequestValues(req) {
			first, err := AppendValue(nil, v)
			require.NoError(t, err)
			back, err := DecodeValue(first, NewBudget(DefaultMaxDecodeBytes))
			require.NoError(t, err, "hand encoding of %#v does not decode", v)
			second, err := AppendValue(nil, back)
			require.NoError(t, err)
			require.True(t, bytes.Equal(first, second), "value codec is not idempotent for %#v", v)
		}
	})
}
