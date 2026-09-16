package montygo_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox"
	"github.com/asalimonov/montygo/sandbox/host"
	"github.com/stretchr/testify/require"
)

type recordDTO struct {
	ID   int    `monty:"id"`
	Name string `monty:"name"`
}

type recordTable struct{}

func (*recordTable) Insert(name string) recordDTO { return recordDTO{ID: 1, Name: name} }
func (*recordTable) Read(id int) *recordDTO {
	if id != 1 {
		return nil
	}
	return &recordDTO{ID: 1, Name: "Ada"}
}
func (*recordTable) Later(ctx context.Context) *host.Future {
	return host.AsyncContext(ctx, func(context.Context) (any, error) {
		return recordDTO{ID: 2, Name: "Grace"}, nil
	})
}

type recordsAPI interface {
	Insert(string) recordDTO
	Read(int) *recordDTO
	Later(context.Context) *host.Future
}

func recordResult(_ string, v any) (any, error) {
	switch record := v.(type) {
	case recordDTO:
		return host.AsNamedTuple(record)
	case *recordDTO:
		if record == nil {
			return nil, nil
		}
		return host.AsNamedTuple(*record)
	default:
		return v, nil
	}
}

func recordHost() (*host.Host, error) {
	h := host.NewHost()
	err := h.Object("records", &recordTable{}, host.ClassInstanceOptions{
		Name: "Records", AllowedMethods: host.Expose[recordsAPI](), ConvertValue: recordResult,
	})
	return h, err
}

func Example_recordConversion() {
	ctx := context.Background()
	pool, _ := montygo.NewPool(ctx, montygo.PoolOptions{})
	defer pool.Close(ctx)
	host, _ := recordHost()
	session, _ := pool.Checkout(ctx, mustRuntime(montygo.RuntimeOptions{Host: host}), montygo.CheckoutOptions{})
	defer session.Close(ctx)
	value, _ := session.FeedRun(ctx, "[records.insert('Ada').name, records.read(99) is None, (await records.later()).name]", nil)
	fmt.Println(value)
	// Output: [Ada true Grace]
}

func TestRecordConversionRecipe(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		host, err := recordHost()
		require.NoError(t, err)
		s := newSessionRT(t, b, mustRuntime(montygo.RuntimeOptions{Host: host}), montygo.CheckoutOptions{})
		value, err := s.FeedRun(testCtx(t), "[records.insert('Ada').name, records.read(1).id, records.read(99) is None, (await records.later()).name]", nil)
		require.NoError(t, err)
		require.Equal(t, []any{"Ada", int64(1), true, "Grace"}, value)
		unchanged := sandbox.DateTime{}
		converted, err := recordResult("date", unchanged)
		require.NoError(t, err)
		require.Equal(t, unchanged, converted)
	})
}

type nilFutureTable struct{}

func (*nilFutureTable) Later() *host.Future { return nil }

func TestNilMethodFutureIsReported(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		wrapped := host.MustClassInstance(&nilFutureTable{}, host.ClassInstanceOptions{AllowedMethods: host.Names("later")})
		_, err := run(t, b, "await table.later()", runOptions{FeedOptions: montygo.FeedOptions{Inputs: map[string]any{"table": wrapped}}})
		var runtimeErr *monterr.RuntimeError
		require.ErrorAs(t, err, &runtimeErr)
		require.Contains(t, runtimeErr.Message, "nil Future")
	})
}
