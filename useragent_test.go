package montygo

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo/internal/worker"
)

func TestUserAgent(t *testing.T) {
	require.Equal(t, worker.DefaultUserAgent+" montygo/"+BindingVersion(), userAgent())
	require.True(t, strings.HasPrefix(userAgent(), "monty-pool/"+MontyVersion+" montygo/"))
}
