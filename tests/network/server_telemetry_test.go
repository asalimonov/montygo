package network

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

const (
	testTraceID      = "4bf92f3577b34da6a3ce929d0e0e4736"
	testParentSpanID = "00f067aa0ba902b7"
)

func TestTelemetry_TraceparentParentsConnectionSpan(t *testing.T) {
	t.Parallel()
	receiver := startOTLPReceiver(t)
	s := SetupServer(t, WithArgs("--otlp-endpoint", receiver.Endpoint()))
	ctx := testCtx(t)
	p := s.NewPool(montygo.WebSocketOptions{
		ConnectHeaders: func(context.Context) (map[string]string, error) {
			return map[string]string{"traceparent": "00-" + testTraceID + "-" + testParentSpanID + "-01"}, nil
		},
	})
	session, err := p.Checkout(ctx, montygo.CheckoutOptions{})
	require.NoError(t, err)
	_, err = session.FeedRun(ctx, "1 + 1", nil)
	require.NoError(t, err)
	require.NoError(t, session.Close(ctx))

	connection := receiver.WaitSpan(t, "monty-server connection", 30*time.Second)
	require.Equal(t, testTraceID, hex.EncodeToString(connection.GetTraceId()))
	require.Equal(t, testParentSpanID, hex.EncodeToString(connection.GetParentSpanId()))

	sessionSpan := receiver.WaitSpan(t, "session {script_name}", 30*time.Second)
	require.Equal(t, testTraceID, hex.EncodeToString(sessionSpan.GetTraceId()))
	require.Equal(t, hex.EncodeToString(connection.GetSpanId()), hex.EncodeToString(sessionSpan.GetParentSpanId()))
}

func TestTelemetry_PolicyEventOnConnectionSpan(t *testing.T) {
	t.Parallel()
	receiver := startOTLPReceiver(t)
	s := SetupServer(t, WithArgs("--otlp-endpoint", receiver.Endpoint(), "--idle-timeout", "1"))
	ctx := testCtx(t)
	p := s.NewPool(montygo.WebSocketOptions{})
	session := s.Checkout(ctx, p, montygo.CheckoutOptions{})
	_, err := session.FeedRun(ctx, "1", nil)
	require.NoError(t, err)

	connection := receiver.WaitSpan(t, "monty-server connection", 30*time.Second)
	var detail string
	for _, event := range connection.GetEvents() {
		if event.GetName() == "timeout" {
			for _, attr := range event.GetAttributes() {
				if attr.GetKey() == "detail" {
					detail = attr.GetValue().GetStringValue()
				}
			}
		}
	}
	require.Equal(t, "idle", detail)
}
