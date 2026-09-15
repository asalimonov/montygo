package wire

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/asalimonov/montygo/internal/value"
	pb "github.com/asalimonov/montygo/montypb"
)

func ptr[T any](v T) *T { return &v }

const testUUID = "0af76519-16cd-43dd-8448-eb211c80319c"

func testUUIDBytes() []byte {
	b, _ := ParseUUID(testUUID)
	return b
}

func pbInt(i int64) *pb.MontyObject { return &pb.MontyObject{Kind: &pb.MontyObject_Int{Int: i}} }
func pbStr(s string) *pb.MontyObject {
	return &pb.MontyObject{Kind: &pb.MontyObject_Str{Str: s}}
}

type codecCase struct {
	name string
	goV  any
	pbV  *pb.MontyObject
}

func codecCases() []codecCase {
	huge, _ := new(big.Int).SetString("-123456789012345678901234567890", 10)
	d := &value.Dict{}
	d.Append("a", int64(1))
	d.Append(int64(2), value.Tuple{"x"})
	attrs := &value.Dict{}
	attrs.Append("x", int64(3))
	return []codecCase{
		{"none", nil, &pb.MontyObject{Kind: &pb.MontyObject_None{None: &pb.Unit{}}}},
		{"ellipsis", value.Ellipsis, &pb.MontyObject{Kind: &pb.MontyObject_Ellipsis{Ellipsis: &pb.Unit{}}}},
		{"notimpl", value.NotImplemented, &pb.MontyObject{Kind: &pb.MontyObject_NotImplemented{NotImplemented: &pb.Unit{}}}},
		{"false", false, &pb.MontyObject{Kind: &pb.MontyObject_Boolean{Boolean: false}}},
		{"true", true, &pb.MontyObject{Kind: &pb.MontyObject_Boolean{Boolean: true}}},
		{"int0", int64(0), pbInt(0)},
		{"intneg", int64(-42), pbInt(-42)},
		{"bigint", huge, &pb.MontyObject{Kind: &pb.MontyObject_Bigint{Bigint: &pb.BigInt{Negative: true, Magnitude: new(big.Int).Abs(huge).Bytes()}}}},
		{"float", 1.5, &pb.MontyObject{Kind: &pb.MontyObject_Float{Float: 1.5}}},
		{"str empty", "", pbStr("")},
		{"str", "héllo", pbStr("héllo")},
		{"bytes", []byte{0, 1, 2}, &pb.MontyObject{Kind: &pb.MontyObject_Bytes{Bytes: []byte{0, 1, 2}}}},
		{"list", []any{int64(1), "a"}, &pb.MontyObject{Kind: &pb.MontyObject_List{List: &pb.ObjectList{Items: []*pb.MontyObject{pbInt(1), pbStr("a")}}}}},
		{"empty list", []any{}, &pb.MontyObject{Kind: &pb.MontyObject_List{List: &pb.ObjectList{}}}},
		{"tuple", value.Tuple{int64(1)}, &pb.MontyObject{Kind: &pb.MontyObject_Tuple{Tuple: &pb.ObjectList{Items: []*pb.MontyObject{pbInt(1)}}}}},
		{"namedtuple", value.NamedTuple{TypeName: "os.stat_result", FieldNames: []string{"st_size"}, Values: []any{int64(4)}},
			&pb.MontyObject{Kind: &pb.MontyObject_NamedTuple{NamedTuple: &pb.NamedTuple{TypeName: "os.stat_result", FieldNames: []string{"st_size"}, Values: []*pb.MontyObject{pbInt(4)}}}}},
		{"dict", d, &pb.MontyObject{Kind: &pb.MontyObject_Dict{Dict: &pb.Dict{Pairs: []*pb.Pair{
			{Key: pbStr("a"), Value: pbInt(1)},
			{Key: pbInt(2), Value: &pb.MontyObject{Kind: &pb.MontyObject_Tuple{Tuple: &pb.ObjectList{Items: []*pb.MontyObject{pbStr("x")}}}}},
		}}}}},
		{"set", func() *value.Set { s := &value.Set{}; s.Append(int64(1)); return s }(), &pb.MontyObject{Kind: &pb.MontyObject_Set{Set: &pb.ObjectList{Items: []*pb.MontyObject{pbInt(1)}}}}},
		{"frozenset", func() *value.FrozenSet { s := &value.FrozenSet{}; s.Append("q"); return s }(), &pb.MontyObject{Kind: &pb.MontyObject_FrozenSet{FrozenSet: &pb.ObjectList{Items: []*pb.MontyObject{pbStr("q")}}}}},
		{"date", value.Date{Year: 2024, Month: 2, Day: 29}, &pb.MontyObject{Kind: &pb.MontyObject_Date{Date: &pb.Date{Year: 2024, Month: 2, Day: 29}}}},
		{"time aware", value.Time{Hour: 10, Minute: 30, Second: 5, Microsecond: 12, OffsetSeconds: ptr(int32(-18000)), TimezoneName: ptr("EST"), Fold: 1},
			&pb.MontyObject{Kind: &pb.MontyObject_Time{Time: &pb.Time{Hour: 10, Minute: 30, Second: 5, Microsecond: 12, OffsetSeconds: ptr(int32(-18000)), TimezoneName: ptr("EST"), Fold: 1}}}},
		{"datetime naive", value.DateTime{Year: 2020, Month: 1, Day: 2, Hour: 3}, &pb.MontyObject{Kind: &pb.MontyObject_Datetime{Datetime: &pb.DateTime{Year: 2020, Month: 1, Day: 2, Hour: 3}}}},
		{"datetime zero offset", value.DateTime{Year: 2020, Month: 1, Day: 2, OffsetSeconds: ptr(int32(0))}, &pb.MontyObject{Kind: &pb.MontyObject_Datetime{Datetime: &pb.DateTime{Year: 2020, Month: 1, Day: 2, OffsetSeconds: ptr(int32(0))}}}},
		{"timedelta", value.TimeDelta{Days: -1, Seconds: 86399, Microseconds: 5}, &pb.MontyObject{Kind: &pb.MontyObject_Timedelta{Timedelta: &pb.TimeDelta{Days: -1, Seconds: 86399, Microseconds: 5}}}},
		{"timezone", value.TimeZone{OffsetSeconds: 3600, Name: ptr("CET")}, &pb.MontyObject{Kind: &pb.MontyObject_Timezone{Timezone: &pb.TimeZone{OffsetSeconds: 3600, Name: ptr("CET")}}}},
		{"exception", value.Exception{ExcType: "ValueError", Message: "bad"}, &pb.MontyObject{Kind: &pb.MontyObject_Exception{Exception: &pb.Exception{ExcType: "ValueError", Arg: ptr("bad")}}}},
		{"builtin type", value.Type{Name: "int", Origin: value.OriginBuiltin}, &pb.MontyObject{Kind: &pb.MontyObject_Type{Type: &pb.Type{Name: "int", Origin: pb.TypeOrigin_TYPE_ORIGIN_BUILTIN}}}},
		{"host type", value.Type{Name: "Point", ID: testUUID, Origin: value.OriginHost, Attrs: attrs},
			&pb.MontyObject{Kind: &pb.MontyObject_Type{Type: &pb.Type{Name: "Point", Id: &pb.Uuid{Data: testUUIDBytes()}, Origin: pb.TypeOrigin_TYPE_ORIGIN_HOST, Attrs: &pb.Dict{Pairs: []*pb.Pair{{Key: pbStr("x"), Value: pbInt(3)}}}}}}},
		{"instance", value.Instance{Type: value.Type{Name: "P", ID: testUUID, Origin: value.OriginSandbox, IsDataclass: true}, ID: testUUID, Attrs: attrs},
			&pb.MontyObject{Kind: &pb.MontyObject_ClassInstance{ClassInstance: &pb.ClassInstance{
				Type:       &pb.Type{Name: "P", Id: &pb.Uuid{Data: testUUIDBytes()}, Origin: pb.TypeOrigin_TYPE_ORIGIN_SANDBOX, IsDataclass: true},
				InstanceId: &pb.Uuid{Data: testUUIDBytes()},
				Attrs:      &pb.Dict{Pairs: []*pb.Pair{{Key: pbStr("x"), Value: pbInt(3)}}},
			}}}},
		{"function", value.Function{Name: "f", Docstring: ptr("doc")}, &pb.MontyObject{Kind: &pb.MontyObject_Function{Function: &pb.Function{Name: "f", Docstring: ptr("doc")}}}},
		{"builtin fn", value.BuiltinFunction("len"), &pb.MontyObject{Kind: &pb.MontyObject_BuiltinFunction{BuiltinFunction: "len"}}},
		{"path", value.Path("/a/b"), &pb.MontyObject{Kind: &pb.MontyObject_Path{Path: "/a/b"}}},
		{"file handle", &value.FileHandle{Path: "/x", Mode: "rb", Position: 7}, &pb.MontyObject{Kind: &pb.MontyObject_FileHandle{FileHandle: &pb.FileHandle{Path: "/x", Mode: "rb", Position: 7}}}},
	}
}

func TestValueEncodeMatchesGenerated(t *testing.T) {
	for _, c := range codecCases() {
		t.Run(c.name, func(t *testing.T) {
			ours, err := AppendValue(nil, c.goV)
			require.NoError(t, err)
			got := &pb.MontyObject{}
			require.NoError(t, proto.Unmarshal(ours, got))
			require.True(t, proto.Equal(c.pbV, got), "want %v\ngot  %v", c.pbV, got)
		})
	}
}

func TestValueDecodeMatchesGenerated(t *testing.T) {
	for _, c := range codecCases() {
		t.Run(c.name, func(t *testing.T) {
			raw, err := proto.Marshal(c.pbV)
			require.NoError(t, err)
			got, err := DecodeValue(raw, NewBudget(DefaultMaxDecodeBytes))
			require.NoError(t, err)
			require.True(t, value.Equal(c.goV, got) || sameShape(c.goV, got), "want %#v got %#v", c.goV, got)
		})
	}
}

func sameShape(a, b any) bool {
	ea, _ := AppendValue(nil, a)
	eb, _ := AppendValue(nil, b)
	return string(ea) == string(eb)
}

func TestReprAndCycleDecode(t *testing.T) {
	raw, _ := proto.Marshal(&pb.MontyObject{Kind: &pb.MontyObject_Repr{Repr: "<C object at 0x5>"}})
	v, err := DecodeValue(raw, nil)
	require.NoError(t, err)
	require.Equal(t, "<C object at 0x5>", v)
	raw, _ = proto.Marshal(&pb.MontyObject{Kind: &pb.MontyObject_Cycle{Cycle: &pb.Cycle{Identity: 9, Placeholder: "[...]"}}})
	v, err = DecodeValue(raw, nil)
	require.NoError(t, err)
	require.Equal(t, value.Cycle{Identity: 9, Placeholder: "[...]"}, v)
}

func TestDecodeBudget(t *testing.T) {
	items := make([]*pb.MontyObject, 1000)
	for i := range items {
		items[i] = &pb.MontyObject{Kind: &pb.MontyObject_None{None: &pb.Unit{}}}
	}
	raw, _ := proto.Marshal(&pb.MontyObject{Kind: &pb.MontyObject_List{List: &pb.ObjectList{Items: items}}})
	_, err := DecodeValue(raw, NewBudget(10_000))
	require.ErrorIs(t, err, ErrDecodeBudget)
}

func TestDecodeRejectsInvalid(t *testing.T) {
	bad := []*pb.MontyObject{
		{Kind: &pb.MontyObject_Date{Date: &pb.Date{Year: 2023, Month: 2, Day: 29}}},
		{Kind: &pb.MontyObject_Type{Type: &pb.Type{Name: "int"}}},
		{Kind: &pb.MontyObject_Type{Type: &pb.Type{Name: "P", Origin: pb.TypeOrigin_TYPE_ORIGIN_HOST}}},
		{Kind: &pb.MontyObject_Time{Time: &pb.Time{TimezoneName: ptr("X")}}},
		{},
	}
	for _, b := range bad {
		raw, _ := proto.Marshal(b)
		_, err := DecodeValue(raw, nil)
		require.Error(t, err, "%v", b)
	}
}

func TestEncodeRequests(t *testing.T) {
	enc := func(req Request, trace string) *pb.ParentRequest {
		raw, err := EncodeRequest(req, trace)
		require.NoError(t, err)
		got := &pb.ParentRequest{}
		require.NoError(t, proto.Unmarshal(raw, got))
		return got
	}
	got := enc(Configure{ScriptName: "main.py", MontyVersion: "0.0.23", ProtocolVersion: 3, TypeCheck: true, TypeCheckStubs: ptr("x"),
		AssertMessageAnnotations: ptr(uint32(0)), TypeCheckFormat: 4, TypeCheckColor: true, PrintFlushIntervalMs: ptr(uint32(0)),
		Limits: &Limits{MaxRecursionDepth: ptr(uint64(1000)), MaxSuspensions: ptr(uint64(0))}}, "00-abc")
	want := &pb.ParentRequest{Kind: &pb.ParentRequest_Configure{Configure: &pb.Configure{
		ScriptName: "main.py", MontyVersion: "0.0.23", ProtocolVersion: 3, TypeCheck: true, TypeCheckStubs: ptr("x"),
		AssertMessageAnnotations: ptr(uint32(0)), TypeCheckFormat: pb.TypeCheckFormat_TYPE_CHECK_FORMAT_JSON, TypeCheckColor: true,
		PrintFlushIntervalMs: ptr(uint32(0)), Limits: &pb.ResourceLimits{MaxRecursionDepth: ptr(uint64(1000)), MaxSuspensions: ptr(uint64(0))},
	}}, TraceParent: ptr("00-abc")}
	require.True(t, proto.Equal(want, got), "%v", got)

	got = enc(Feed{Code: "x", Inputs: []NamedValue{{Name: "a", Value: int64(1)}}, SkipTypeCheck: true, Cwd: "/data"}, "")
	want = &pb.ParentRequest{Kind: &pb.ParentRequest_Feed{Feed: &pb.Feed{Code: "x", Inputs: []*pb.NamedValue{{Name: "a", Value: pbInt(1)}}, SkipTypeCheck: true, Cwd: "/data"}}}
	require.True(t, proto.Equal(want, got), "%v", got)

	exc := NewException("ValueError", "nope")
	got = enc(ResumeCall{CallID: 3, Result: ExtResult{Kind: ExtError, Error: exc}}, "")
	want = &pb.ParentRequest{Kind: &pb.ParentRequest_ResumeCall{ResumeCall: &pb.ResumeCall{CallId: 3, Result: &pb.ExtFunctionResult{Kind: &pb.ExtFunctionResult_Error{Error: &pb.RaisedException{ExcType: "ValueError", Message: ptr("nope")}}}}}}
	require.True(t, proto.Equal(want, got), "%v", got)

	for _, r := range []struct {
		res  ExtResult
		want *pb.ExtFunctionResult
	}{
		{ExtResult{Kind: ExtReturn, Value: "v"}, &pb.ExtFunctionResult{Kind: &pb.ExtFunctionResult_ReturnValue{ReturnValue: pbStr("v")}}},
		{ExtResult{Kind: ExtFuture, FutureCallID: 0}, &pb.ExtFunctionResult{Kind: &pb.ExtFunctionResult_Future{Future: 0}}},
		{ExtResult{Kind: ExtNotFound, NotFoundName: "f"}, &pb.ExtFunctionResult{Kind: &pb.ExtFunctionResult_NotFound{NotFound: "f"}}},
		{ExtResult{Kind: ExtNotHandled}, &pb.ExtFunctionResult{Kind: &pb.ExtFunctionResult_NotHandled{NotHandled: &pb.Unit{}}}},
	} {
		got = enc(ResumeFutures{Results: []FutureResult{{CallID: 0, Result: r.res}}}, "")
		want = &pb.ParentRequest{Kind: &pb.ParentRequest_ResumeFutures{ResumeFutures: &pb.ResumeFutures{Results: []*pb.FutureResult{{CallId: 0, Result: r.want}}}}}
		require.True(t, proto.Equal(want, got), "%v", got)
	}

	got = enc(ResumeNameLookup{Kind: LookupUndefined}, "")
	want = &pb.ParentRequest{Kind: &pb.ParentRequest_ResumeNameLookup{ResumeNameLookup: &pb.ResumeNameLookup{Kind: &pb.ResumeNameLookup_Undefined{Undefined: &pb.Unit{}}}}}
	require.True(t, proto.Equal(want, got), "%v", got)

	got = enc(Load{State: []byte("abc")}, "")
	want = &pb.ParentRequest{Kind: &pb.ParentRequest_Load{Load: &pb.Load{State: []byte("abc")}}}
	require.True(t, proto.Equal(want, got), "%v", got)

	got = enc(AbortFeed{Exception: exc}, "")
	want = &pb.ParentRequest{Kind: &pb.ParentRequest_AbortFeed{AbortFeed: &pb.AbortFeed{Exception: &pb.RaisedException{ExcType: "ValueError", Message: ptr("nope")}}}}
	require.True(t, proto.Equal(want, got), "%v", got)

	got = enc(InstallDependencies{Requirements: []string{"a", "b"}}, "")
	want = &pb.ParentRequest{Kind: &pb.ParentRequest_InstallDependencies{InstallDependencies: &pb.InstallDependencies{Requirements: []string{"a", "b"}}}}
	require.True(t, proto.Equal(want, got), "%v", got)
}

func TestDecodeEvents(t *testing.T) {
	dec := func(ev *pb.ChildEvent) *Event {
		raw, err := proto.Marshal(ev)
		require.NoError(t, err)
		got, err := DecodeEvent(raw)
		require.NoError(t, err)
		return got
	}
	ev := dec(&pb.ChildEvent{Kind: &pb.ChildEvent_FunctionCall{FunctionCall: &pb.FunctionCall{
		FunctionName: "fetch", Args: []*pb.MontyObject{pbInt(1)}, Kwargs: []*pb.Pair{{Key: pbStr("k"), Value: pbStr("v")}},
		CallId: 7, ObjectId: &pb.Uuid{Data: testUUIDBytes()}, AllowEagerAwait: true,
	}}, TotalExecutionMicros: 99, MaxDurationMicros: ptr(uint64(5)), MaxSuspensions: ptr(uint64(10)), RestoredScriptName: ptr("s.py")})
	require.Equal(t, EventFunctionCall, ev.Kind)
	require.Equal(t, &FunctionCall{FunctionName: "fetch", Args: []any{int64(1)}, Kwargs: []value.Pair{{Key: "k", Value: "v"}}, CallID: 7, ObjectID: testUUID, AllowEagerAwait: true}, ev.FunctionCall)
	require.Equal(t, uint64(99), ev.TotalExecutionMicros)
	require.Equal(t, uint64(5), *ev.MaxDurationMicros)
	require.Equal(t, uint64(10), *ev.MaxSuspensions)
	require.Equal(t, "s.py", *ev.RestoredScriptName)

	ev = dec(&pb.ChildEvent{Kind: &pb.ChildEvent_Print{Print: &pb.Print{Segments: []*pb.PrintSegment{{Stream: pb.PrintStream_PRINT_STREAM_STDOUT, Text: "a"}, {Stream: pb.PrintStream_PRINT_STREAM_STDERR, Text: "b"}}}}})
	require.Equal(t, []PrintSegment{{Stream: 1, Text: "a"}, {Stream: 2, Text: "b"}}, ev.Print)

	ev = dec(&pb.ChildEvent{Kind: &pb.ChildEvent_ResolveFutures{ResolveFutures: &pb.ResolveFutures{PendingCallIds: []uint32{1, 2, 300}}}})
	require.Equal(t, []uint32{1, 2, 300}, ev.PendingCallIDs)

	ev = dec(&pb.ChildEvent{Kind: &pb.ChildEvent_NameLookup{NameLookup: &pb.NameLookup{Name: "x"}}})
	require.Equal(t, &NameLookup{Name: "x"}, ev.NameLookup)

	ev = dec(&pb.ChildEvent{Kind: &pb.ChildEvent_Complete{Complete: &pb.Complete{Value: pbInt(42)}}})
	require.Equal(t, EventComplete, ev.Kind)
	require.True(t, ev.HasValue)
	require.Equal(t, int64(42), ev.Value)

	ev = dec(&pb.ChildEvent{Kind: &pb.ChildEvent_Error{Error: &pb.Error{Exception: &pb.RaisedException{ExcType: "ZeroDivisionError", Message: ptr("division by zero"), Traceback: []*pb.StackFrame{{
		Filename: "<python-input-1>", Start: &pb.CodeLoc{Line: 1, Column: 1}, End: &pb.CodeLoc{Line: 1, Column: 6}, PreviewLine: ptr("1 / 0"),
	}}}}}})
	require.Equal(t, "Traceback (most recent call last):\n  File \"<python-input-1>\", line 1, in <module>\n    1 / 0\n    ~~~~~\nZeroDivisionError: division by zero", ev.Exception.Render())

	for _, oc := range []struct {
		pb   *pb.OsCall
		want OsCall
		name string
	}{
		{&pb.OsCall{CallId: 1, Call: &pb.OsCall_ReadText{ReadText: "/a"}}, OsCall{CallID: 1, Op: OpReadText, Path: "/a"}, "Path.read_text"},
		{&pb.OsCall{Call: &pb.OsCall_WriteBytes{WriteBytes: &pb.OsCall_BytesWrite{Path: "/b", Data: []byte("x")}}}, OsCall{Op: OpWriteBytes, Path: "/b", Data: []byte("x")}, "Path.write_bytes"},
		{&pb.OsCall{Call: &pb.OsCall_AppendText{AppendText: &pb.OsCall_TextWrite{Path: "/b", Data: "x"}}}, OsCall{Op: OpAppendText, Path: "/b", Text: "x"}, "Path.append_text"},
		{&pb.OsCall{Call: &pb.OsCall_Mkdir_{Mkdir: &pb.OsCall_Mkdir{Path: "/d", Parents: true}}}, OsCall{Op: OpMkdir, Path: "/d", Parents: true}, "Path.mkdir"},
		{&pb.OsCall{Call: &pb.OsCall_Rename_{Rename: &pb.OsCall_Rename{Src: "/s", Dst: "/t"}}}, OsCall{Op: OpRename, Path: "/s", Dst: "/t"}, "Path.rename"},
		{&pb.OsCall{Call: &pb.OsCall_Getenv_{Getenv: &pb.OsCall_Getenv{Key: "HOME", Default: pbStr("d")}}}, OsCall{Op: OpGetenv, Key: "HOME", Default: "d"}, "os.getenv"},
		{&pb.OsCall{Call: &pb.OsCall_GetEnviron{GetEnviron: &pb.Unit{}}}, OsCall{Op: OpGetEnviron}, "os.environ"},
		{&pb.OsCall{Call: &pb.OsCall_DateTimeNow_{DateTimeNow: &pb.OsCall_DateTimeNow{Tz: &pb.TimeZone{OffsetSeconds: 60}}}}, OsCall{Op: OpDateTimeNow, HasTZ: true, TZ: value.TimeZone{OffsetSeconds: 60}}, "datetime.now"},
	} {
		ev = dec(&pb.ChildEvent{Kind: &pb.ChildEvent_OsCall{OsCall: oc.pb}})
		require.Equal(t, oc.want, *ev.OsCall)
		require.Equal(t, oc.name, ev.OsCall.Name())
	}
}
