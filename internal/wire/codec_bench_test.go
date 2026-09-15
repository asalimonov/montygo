package wire

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/asalimonov/montygo/montypb"
)

func mustMarshal(b *testing.B, m proto.Message) []byte {
	b.Helper()
	raw, err := proto.Marshal(m)
	if err != nil {
		b.Fatal(err)
	}
	return raw
}

func benchDecodeEvent(b *testing.B, raw []byte) {
	b.Run("hand", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(raw)))
		for b.Loop() {
			if _, err := DecodeEvent(raw); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("generated", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(raw)))
		for b.Loop() {
			if err := proto.Unmarshal(raw, &pb.ChildEvent{}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkDecodeEventPrint(b *testing.B) {
	text := strings.Repeat("print output line\n", (64<<10)/len("print output line\n"))
	raw := mustMarshal(b, &pb.ChildEvent{Kind: &pb.ChildEvent_Print{Print: &pb.Print{
		Segments: []*pb.PrintSegment{{Stream: pb.PrintStream_PRINT_STREAM_STDOUT, Text: text}},
	}}})
	benchDecodeEvent(b, raw)
}

func BenchmarkDecodeEventDeepValue(b *testing.B) {
	v := pbInt(1)
	for range 30 {
		v = &pb.MontyObject{Kind: &pb.MontyObject_List{List: &pb.ObjectList{Items: []*pb.MontyObject{v}}}}
	}
	raw := mustMarshal(b, &pb.ChildEvent{Kind: &pb.ChildEvent_Complete{Complete: &pb.Complete{Value: v}}, TotalExecutionMicros: 1234})
	benchDecodeEvent(b, raw)
}

func BenchmarkDecodeEventComplete(b *testing.B) {
	rows := make([]*pb.MontyObject, 0, 200)
	for i := range 200 {
		row := &pb.Dict{Pairs: []*pb.Pair{
			{Key: pbStr("id"), Value: pbInt(int64(i))},
			{Key: pbStr("name"), Value: pbStr("customer-" + strings.Repeat("x", i%17))},
			{Key: pbStr("score"), Value: &pb.MontyObject{Kind: &pb.MontyObject_Float{Float: float64(i) / 7}}},
			{Key: pbStr("active"), Value: &pb.MontyObject{Kind: &pb.MontyObject_Boolean{Boolean: i%2 == 0}}},
			{Key: pbStr("tags"), Value: &pb.MontyObject{Kind: &pb.MontyObject_List{List: &pb.ObjectList{Items: []*pb.MontyObject{pbStr("a"), pbStr("b"), pbStr("c")}}}}},
			{Key: pbStr("when"), Value: &pb.MontyObject{Kind: &pb.MontyObject_Datetime{Datetime: &pb.DateTime{Year: 2026, Month: 9, Day: 15, Hour: 12, OffsetSeconds: ptr(int32(0))}}}},
		}}
		rows = append(rows, &pb.MontyObject{Kind: &pb.MontyObject_Dict{Dict: row}})
	}
	raw := mustMarshal(b, &pb.ChildEvent{
		Kind:                 &pb.ChildEvent_Complete{Complete: &pb.Complete{Value: &pb.MontyObject{Kind: &pb.MontyObject_List{List: &pb.ObjectList{Items: rows}}}}},
		TotalExecutionMicros: 98765,
		MaxDurationMicros:    ptr(uint64(10_000_000)),
		MaxSuspensions:       ptr(uint64(1000)),
	})
	benchDecodeEvent(b, raw)
}

func BenchmarkEncodeRequestFeed16MiB(b *testing.B) {
	code := strings.Repeat("x = 1\n", (16<<20)/len("x = 1\n"))
	feed := Feed{Code: code, Inputs: []NamedValue{{Name: "n", Value: int64(1)}}, Cwd: "/data"}
	pbFeed := &pb.ParentRequest{Kind: &pb.ParentRequest_Feed{Feed: &pb.Feed{Code: code, Inputs: []*pb.NamedValue{{Name: "n", Value: pbInt(1)}}, Cwd: "/data"}}}
	b.Run("hand", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(code)))
		for b.Loop() {
			if _, err := EncodeRequest(feed, ""); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("generated", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(code)))
		for b.Loop() {
			if _, err := proto.Marshal(pbFeed); err != nil {
				b.Fatal(err)
			}
		}
	})
}
