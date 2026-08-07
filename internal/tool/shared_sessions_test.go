package tool_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/tool"
)

// bgCall runs the bash tool with background=true and returns its text.
func bgCall(t *testing.T, r *tool.Registry, command string) string {
	t.Helper()
	bash, ok := r.Get("bash")
	require.True(t, ok, "bash tool missing")
	out, err := bash.Execute(context.Background(), map[string]any{
		"command":    command,
		"background": true,
	}, nil)
	require.NoError(t, err)
	return resultText(out)
}

func procCall(t *testing.T, r *tool.Registry, args map[string]any) string {
	t.Helper()
	proc, ok := r.Get("process")
	require.True(t, ok, "process tool missing")
	out, err := proc.Execute(context.Background(), args, nil)
	require.NoError(t, err)
	return resultText(out)
}

// TestSharedSessionsAreScopedPerAria is the isolation boundary. One
// registry serves both arias; neither may see the other's sessions.
//
// This is the regression that hoisting the registry to the daemon would
// otherwise introduce: the scope used to be "default" for every aria, and
// only the privacy of each agent's own registry hid it.
func TestSharedSessionsAreScopedPerAria(t *testing.T) {
	shared := tool.NewSessionRegistry(tool.DefaultSessionTTL)
	cwd := func() string { return t.TempDir() }

	a := tool.DefaultRegistryForAria("aria-a", cwd, tool.WithSessions(shared))
	b := tool.DefaultRegistryForAria("aria-b", cwd, tool.WithSessions(shared))
	t.Cleanup(func() {
		shared.KillScope("aria-a")
		shared.KillScope("aria-b")
	})

	bgCall(t, a, "sleep 30")
	bgCall(t, b, "sleep 30")

	require.Len(t, shared.List("aria-a"), 1, "aria-a session count")
	require.Len(t, shared.List("aria-b"), 1, "aria-b session count")

	// And through the tool surface, which is what the model actually sees.
	listA := procCall(t, a, map[string]any{"action": "list"})
	require.Equal(t, 1, strings.Count(listA, "bg-"),
		"aria-a process list leaked across scopes:\n%s", listA)
}

// TestSharedSessionsSeqIsGlobal: ids must never collide across arias or
// across a rebuild of one aria's tool registry. A per-agent registry
// restarted seq at zero, so a wake minted a bg-1 that an orphaned session
// already answered to.
func TestSharedSessionsSeqIsGlobal(t *testing.T) {
	shared := tool.NewSessionRegistry(tool.DefaultSessionTTL)
	cwd := func() string { return t.TempDir() }

	a := tool.DefaultRegistryForAria("aria-a", cwd, tool.WithSessions(shared))
	b := tool.DefaultRegistryForAria("aria-b", cwd, tool.WithSessions(shared))
	t.Cleanup(func() {
		shared.KillScope("aria-a")
		shared.KillScope("aria-b")
	})

	seen := map[string]bool{}
	for _, r := range []*tool.Registry{a, b, a, b} {
		out := bgCall(t, r, "sleep 30")
		id := extractSessionID(out)
		require.NotEmpty(t, id, "no session id in bash output:\n%s", out)
		require.False(t, seen[id], "session id %q minted twice", id)
		seen[id] = true
	}
}

// TestSessionsSurviveToolRegistryRebuild is hibernation in miniature: the
// agent's whole tool registry is discarded and rebuilt, and the
// background job is still there, under the same id, still readable.
func TestSessionsSurviveToolRegistryRebuild(t *testing.T) {
	shared := tool.NewSessionRegistry(tool.DefaultSessionTTL)
	dir := t.TempDir()
	cwd := func() string { return dir }

	before := tool.DefaultRegistryForAria("aria-a", cwd, tool.WithSessions(shared))
	out := bgCall(t, before, "for i in 1 2 3; do echo tick-$i; sleep 0.1; done; sleep 30")
	id := extractSessionID(out)
	require.NotEmpty(t, id, "no session id:\n%s", out)
	t.Cleanup(func() { shared.KillScope("aria-a") })

	// The agent dies: only the tool registry is dropped. The daemon's
	// session registry is untouched, which is the whole point.
	after := tool.DefaultRegistryForAria("aria-a", cwd, tool.WithSessions(shared))

	var log string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		log = procCall(t, after, map[string]any{"action": "log", "session": id})
		if strings.Contains(log, "tick-3") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s not readable after rebuild:\n%s", id, log)
}

// TestKillScopeSparesOtherScopes: deleting one aria must not touch a
// neighbour's running jobs.
func TestKillScopeSparesOtherScopes(t *testing.T) {
	shared := tool.NewSessionRegistry(tool.DefaultSessionTTL)
	cwd := func() string { return t.TempDir() }

	a := tool.DefaultRegistryForAria("aria-a", cwd, tool.WithSessions(shared))
	b := tool.DefaultRegistryForAria("aria-b", cwd, tool.WithSessions(shared))
	t.Cleanup(func() { shared.KillScope("aria-b") })

	bgCall(t, a, "sleep 30")
	bgCall(t, b, "sleep 30")

	require.Equal(t, 1, shared.KillScope("aria-a"), "KillScope(aria-a) count")
	require.Empty(t, shared.List("aria-a"), "aria-a sessions after kill")
	require.Len(t, shared.List("aria-b"), 1, "aria-b lost sessions to a neighbour's kill")
}

// TestPrivateSessionsByDefault: a caller that passes no registry still
// gets its own, so nothing outside the daemon changes behaviour.
func TestPrivateSessionsByDefault(t *testing.T) {
	cwd := func() string { return t.TempDir() }
	a := tool.DefaultRegistryForAria("aria-a", cwd)
	b := tool.DefaultRegistryForAria("aria-b", cwd)

	bgCall(t, a, "sleep 30")
	listB := procCall(t, b, map[string]any{"action": "list"})
	require.NotContains(t, listB, "bg-", "private registries must not share")
}

func extractSessionID(s string) string {
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
