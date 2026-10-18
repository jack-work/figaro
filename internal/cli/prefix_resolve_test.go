package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// A prefix is an id everywhere the door resolves one. status and an
// env-attended ls once matched the typed text against a listing, so they
// refused a prefix that send, show and state accepted (telefono's /status and
// /ls after /at <prefix>).
func prefixRig(t *testing.T) (*config.Loaded, string) {
	t.Helper()
	backend, id := store.NewTestAria(t, "prefix", message.Patch{})
	require.Greater(t, len(id), 8, "the store mints long ids")
	_, dir := serveBackend(t, backend)
	cfg := t.TempDir()
	t.Setenv("FIGARO_RUNTIME_DIR", dir)
	t.Setenv("FIGARO_CONFIG_DIR", cfg)
	t.Setenv("FIGARO_TROUPE", "")
	t.Setenv("FIGARO_NO_BIND", "1")
	loaded, err := config.Load(cfg)
	require.NoError(t, err)
	return loaded, id
}

func stdoutOf(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := stdout
	stdout = &buf
	defer func() { stdout = prev }()
	if code, exited := captureExit(t, fn); exited {
		t.Fatalf("exited %d; stdout so far:\n%s", code, buf.String())
	}
	return buf.String()
}

func statusID(t *testing.T, out string) string {
	t.Helper()
	var row struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &row), "status -j printed %q", out)
	return row.ID
}

func TestStatusResolvesAPrefixArgument(t *testing.T) {
	loaded, id := prefixRig(t)
	out := stdoutOf(t, func() { runStatus(loaded, "", []string{id[:6]}, false, true) })
	require.Equal(t, id, statusID(t, out))
}

func TestStatusResolvesAPrefixFlag(t *testing.T) {
	loaded, id := prefixRig(t)
	out := stdoutOf(t, func() { runStatus(loaded, id[:6], nil, false, true) })
	require.Equal(t, id, statusID(t, out))
}

func TestStatusResolvesAPrefixInFigaroAria(t *testing.T) {
	loaded, id := prefixRig(t)
	t.Setenv("FIGARO_ARIA", id[:6])
	out := stdoutOf(t, func() { runStatus(loaded, "", nil, false, true) })
	require.Equal(t, id, statusID(t, out))
}

func TestListScopesToAPrefixInFigaroAria(t *testing.T) {
	loaded, id := prefixRig(t)
	t.Setenv("FIGARO_ARIA", id[:6])
	out := stdoutOf(t, func() { runList(loaded, lsOpts{limit: 10}) })
	require.True(t, strings.Contains(out, id[:9]), "ls drew no row for %s:\n%s", id, out)
}
