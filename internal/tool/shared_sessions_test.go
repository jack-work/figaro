package tool_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/tool"
)

func bgCall(t *testing.T, r *tool.Registry, command string) string {
	t.Helper()
	bash, ok := r.Get("bash")
	require.True(t, ok)
	out, err := bash.Execute(context.Background(), map[string]any{
		"command": command, "background": true,
	}, nil)
	require.NoError(t, err)
	return resultText(out)
}

func procCall(t *testing.T, r *tool.Registry, args map[string]any) string {
	t.Helper()
	proc, ok := r.Get("process")
	require.True(t, ok)
	out, err := proc.Execute(context.Background(), args, nil)
	require.NoError(t, err)
	return resultText(out)
}

// One registry serving two arias must behave as two registries. The scope
// is the only thing enforcing that, and until this change nothing ever set
// it: ScopeFn was nil, so every aria filed under "default" and only the
// privacy of each agent's own map hid it.
func TestSharedSessionsAreScopedPerAria(t *testing.T) {
	shared := tool.NewSessionRegistry(tool.DefaultSessionTTL)
	cwd := func() string { return t.TempDir() }
	a := tool.DefaultRegistryForAria("aria-a", cwd, tool.WithSessions(shared))
	b := tool.DefaultRegistryForAria("aria-b", cwd, tool.WithSessions(shared))
	t.Cleanup(func() { shared.KillScope("aria-a"); shared.KillScope("aria-b") })

	// Interleaved, and each aria's registry used twice: a per-agent map
	// restarted seq at zero, so a wake minted a bg-1 an orphan still
	// answered to.
	ids := map[string]bool{}
	for _, r := range []*tool.Registry{a, b, a, b} {
		id := sessionID(bgCall(t, r, "sleep 30"))
		require.NotEmpty(t, id)
		require.False(t, ids[id], "session id %q minted twice", id)
		ids[id] = true
	}

	require.Len(t, shared.List("aria-a"), 2)
	require.Len(t, shared.List("aria-b"), 2)
	listA := procCall(t, a, map[string]any{"action": "list"})
	require.Equal(t, 2, strings.Count(listA, "bg-"), "scope leaked:\n%s", listA)

	// Kill is a deletion and takes its own children only.
	require.Equal(t, 2, shared.KillScope("aria-a"))
	require.Empty(t, shared.List("aria-a"))
	require.Len(t, shared.List("aria-b"), 2, "neighbour lost sessions to a kill")
}

// Hibernation in miniature: discard the agent's whole tool registry and
// the job is still running, still under its old id, still readable.
func TestSessionsSurviveToolRegistryRebuild(t *testing.T) {
	shared := tool.NewSessionRegistry(tool.DefaultSessionTTL)
	dir := t.TempDir()
	cwd := func() string { return dir }
	t.Cleanup(func() { shared.KillScope("aria-a") })

	before := tool.DefaultRegistryForAria("aria-a", cwd, tool.WithSessions(shared))
	id := sessionID(bgCall(t, before, "for i in 1 2 3; do echo tick-$i; sleep 0.1; done; sleep 30"))
	require.NotEmpty(t, id)

	after := tool.DefaultRegistryForAria("aria-a", cwd, tool.WithSessions(shared))
	var log string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		log = procCall(t, after, map[string]any{"action": "log", "session": id})
		if strings.Contains(log, "tick-3") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s unreadable after rebuild:\n%s", id, log)
}

func sessionID(s string) string {
	i := strings.Index(s, "bg-")
	if i < 0 {
		return ""
	}
	j := i + 3
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	return s[i:j]
}
