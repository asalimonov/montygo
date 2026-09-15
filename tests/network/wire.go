package network

import (
	"context"
	"errors"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/montypb"
)

func sendRequest(ctx context.Context, c *websocket.Conn, req *montypb.ParentRequest) error {
	body, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageBinary, body)
}

func readEvent(ctx context.Context, c *websocket.Conn) (*montypb.ChildEvent, error) {
	for {
		typ, body, err := c.Read(ctx)
		if err != nil {
			return nil, err
		}
		if typ != websocket.MessageBinary {
			continue
		}
		ev := &montypb.ChildEvent{}
		if err := proto.Unmarshal(body, ev); err != nil {
			return nil, err
		}
		return ev, nil
	}
}

func configureRequest(protocolVersion uint32) *montypb.ParentRequest {
	return &montypb.ParentRequest{Kind: &montypb.ParentRequest_Configure{Configure: &montypb.Configure{
		ScriptName:      "main.py",
		ProtocolVersion: protocolVersion,
	}}}
}

func feedRequest(code string) *montypb.ParentRequest {
	return &montypb.ParentRequest{Kind: &montypb.ParentRequest_Feed{Feed: &montypb.Feed{Code: code, Cwd: "/"}}}
}

func resetRequest() *montypb.ParentRequest {
	return &montypb.ParentRequest{Kind: &montypb.ParentRequest_Reset_{Reset_: &montypb.Reset{}}}
}

// rawConfigured dials and completes a protocol-3 Configure.
func rawConfigured(t *testing.T, ctx context.Context, s *TestServer) *websocket.Conn {
	t.Helper()
	c, _, err := s.RawDial(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.CloseNow() })
	require.NoError(t, sendRequest(ctx, c, configureRequest(montygo.ProtocolVersion)))
	ev, err := readEvent(ctx, c)
	require.NoError(t, err)
	require.NotNil(t, ev.GetOk(), "expected Ok, got %v", ev)
	return c
}

func requireClose(t *testing.T, err error, code websocket.StatusCode, reason string) {
	t.Helper()
	require.Error(t, err)
	var ce websocket.CloseError
	require.True(t, errors.As(err, &ce), "expected a close frame, got %v", err)
	require.Equal(t, code, ce.Code)
	if reason != "" {
		require.Equal(t, reason, ce.Reason)
	}
}
