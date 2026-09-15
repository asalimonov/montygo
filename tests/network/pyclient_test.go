package network

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func pyEnv(t *testing.T, s *TestServer, extra map[string]string) map[string]string {
	t.Helper()
	ip, err := s.Unit.ContainerIP(testCtx(t))
	require.NoError(t, err)
	env := map[string]string{"MONTY_URL": "ws://" + ip + ":8000/"}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func TestPyClient_FeedRunOverWebSocket(t *testing.T) {
	t.Parallel()
	image := pyClientImage(t)
	s := SetupServer(t)
	run := runPyClient(t, image, pythonPinned, "feed_run.py", pyEnv(t, s, nil))
	require.Equal(t, 0, run.ExitCode, run.Output)
	require.Contains(t, run.Output, "OK")
}

func TestPyClient_HostFunctionAndDumpRestore(t *testing.T) {
	t.Parallel()
	image := pyClientImage(t)
	s := SetupServer(t)
	run := runPyClient(t, image, pythonPinned, "host_function_dump_restore.py", pyEnv(t, s, nil))
	require.Equal(t, 0, run.ExitCode, run.Output)
	require.Contains(t, run.Output, "OK 41 42")
}

func TestPyClient_DrainShutdownDumpRestore(t *testing.T) {
	t.Parallel()
	image := pyClientImage(t)
	s := SetupServer(t, WithArgs("--drain-grace", "10"))

	client := startPyClient(t, image, pythonPinned, "drain_capture.py", pyEnv(t, s, nil), "READY")
	s.Signal("TERM")
	captured := waitPyExit(t, client, time.Minute)
	require.Equal(t, 0, captured.ExitCode, captured.Output)
	dump := prefixedLine(t, captured.Output, "DUMP=")

	s.Recreate()
	restored := runPyClient(t, image, pythonPinned, "restore.py", pyEnv(t, s, map[string]string{"MONTY_DUMP": dump}))
	require.Equal(t, 0, restored.ExitCode, restored.Output)
	require.Contains(t, restored.Output, "OK x=1")
}

func TestPyClient_PyPIProtocol2IsRejected(t *testing.T) {
	t.Parallel()
	image := pyClientImage(t)
	s := SetupServer(t)
	run := runPyClient(t, image, pythonPyPI, "protocol_rejected.py", pyEnv(t, s, nil))
	require.Equal(t, 0, run.ExitCode, run.Output)
	require.Contains(t, run.Output, "unsupported protocol version 2 (server supports protocol version 3, try updating to a newer client version)")
}
