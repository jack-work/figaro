package tool_test

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/tool"
)

// bgPair builds a bash tool and a process tool sharing one session
// registry, the way DefaultRegistry wires them.
func bgPair(t *testing.T) (*tool.BashTool, *tool.ProcessTool) {
	t.Helper()
	dir := t.TempDir()
	reg := tool.NewSessionRegistry(tool.DefaultSessionTTL)
	b := tool.NewBashToolWith(func() string { return dir }, tool.NewLocalExecutor(), reg)
	p := tool.NewProcessTool(reg, nil)
	return b, p
}

var sessionRe = regexp.MustCompile(`session (bg-\d+)`)

func extractSession(t *testing.T, out string) string {
	t.Helper()
	m := sessionRe.FindStringSubmatch(out)
	require.Len(t, m, 2, "no session id in output: %s", out)
	return m[1]
}

func proc(t *testing.T, p *tool.ProcessTool, args map[string]any) (string, error) {
	t.Helper()
	res, err := p.Execute(context.Background(), args, nil)
	if err != nil {
		return "", err
	}
	return resultText(res), nil
}

func TestBash_BackgroundsAfterYield(t *testing.T) {
	b, p := bgPair(t)
	res, err := b.Execute(context.Background(), map[string]any{
		"command": "echo started; sleep 5",
		"yieldMs": float64(300),
	}, nil)
	require.NoError(t, err)
	out := resultText(res)
	assert.Contains(t, out, "still running")
	assert.Contains(t, out, "started")

	id := extractSession(t, out)
	defer proc(t, p, map[string]any{"action": "kill", "session": id})

	logOut, err := proc(t, p, map[string]any{"action": "log", "session": id})
	require.NoError(t, err)
	assert.Contains(t, logOut, "started")
	assert.Contains(t, logOut, "running")
}

func TestBash_BackgroundImmediate(t *testing.T) {
	b, p := bgPair(t)
	start := time.Now()
	res, err := b.Execute(context.Background(), map[string]any{
		"command":    "sleep 5",
		"background": true,
	}, nil)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 2*time.Second, "background:true must not wait")
	id := extractSession(t, resultText(res))
	proc(t, p, map[string]any{"action": "kill", "session": id})
}

func TestProcess_PollDrains(t *testing.T) {
	b, p := bgPair(t)
	res, err := b.Execute(context.Background(), map[string]any{
		"command": "echo hello; sleep 5",
		"yieldMs": float64(300),
	}, nil)
	require.NoError(t, err)
	id := extractSession(t, resultText(res))
	defer proc(t, p, map[string]any{"action": "kill", "session": id})

	first, err := proc(t, p, map[string]any{"action": "poll", "session": id})
	require.NoError(t, err)
	assert.Contains(t, pollBody(first), "hello")

	second, err := proc(t, p, map[string]any{"action": "poll", "session": id})
	require.NoError(t, err)
	assert.NotContains(t, pollBody(second), "hello", "poll should drain consumed output")
}

// pollBody strips the leading status line from a poll/log result,
// leaving just the captured output.
func pollBody(out string) string {
	parts := strings.SplitN(out, "\n\n", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return out
}

func TestProcess_Kill(t *testing.T) {
	b, p := bgPair(t)
	res, err := b.Execute(context.Background(), map[string]any{
		"command": "sleep 30",
		"yieldMs": float64(100),
	}, nil)
	require.NoError(t, err)
	id := extractSession(t, resultText(res))

	killOut, err := proc(t, p, map[string]any{"action": "kill", "session": id})
	require.NoError(t, err)
	assert.Contains(t, killOut, "killed")
}

func TestProcess_Write(t *testing.T) {
	b, p := bgPair(t)
	res, err := b.Execute(context.Background(), map[string]any{
		"command": "cat",
		"yieldMs": float64(100),
	}, nil)
	require.NoError(t, err)
	id := extractSession(t, resultText(res))
	defer proc(t, p, map[string]any{"action": "kill", "session": id})

	_, err = proc(t, p, map[string]any{"action": "write", "session": id, "input": "ping"})
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		out, _ := proc(t, p, map[string]any{"action": "poll", "session": id})
		return strings.Contains(out, "ping")
	}, 2*time.Second, 50*time.Millisecond)
}

func TestProcess_ListAndRemove(t *testing.T) {
	b, p := bgPair(t)

	empty, err := proc(t, p, map[string]any{"action": "list"})
	require.NoError(t, err)
	assert.Contains(t, empty, "No background sessions")

	res, err := b.Execute(context.Background(), map[string]any{
		"command": "sleep 30",
		"yieldMs": float64(100),
	}, nil)
	require.NoError(t, err)
	id := extractSession(t, resultText(res))

	listed, err := proc(t, p, map[string]any{"action": "list"})
	require.NoError(t, err)
	assert.Contains(t, listed, id)
	assert.Contains(t, listed, "running")

	removed, err := proc(t, p, map[string]any{"action": "remove", "session": id})
	require.NoError(t, err)
	assert.Contains(t, removed, "Removed")

	_, err = proc(t, p, map[string]any{"action": "poll", "session": id})
	assert.Error(t, err, "removed session should be gone")
}

// TestBash_AbortLeavesBackgroundAlive: once a command is backgrounded,
// cancelling the tool-call context must not kill it.
func TestBash_AbortLeavesBackgroundAlive(t *testing.T) {
	b, p := bgPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	res, err := b.Execute(ctx, map[string]any{
		"command": "sleep 5",
		"yieldMs": float64(100),
	}, nil)
	require.NoError(t, err)
	id := extractSession(t, resultText(res))
	defer proc(t, p, map[string]any{"action": "kill", "session": id})

	cancel()
	time.Sleep(300 * time.Millisecond)

	pollOut, err := proc(t, p, map[string]any{"action": "poll", "session": id})
	require.NoError(t, err)
	assert.Contains(t, pollOut, "running", "abort must not kill a backgrounded session")
}

// TestBash_HardTimeoutKillsBackground: the hard timeout still kills a
// backgrounded session even though abort doesn't.
func TestBash_HardTimeoutKillsBackground(t *testing.T) {
	b, p := bgPair(t)
	res, err := b.Execute(context.Background(), map[string]any{
		"command": "sleep 30",
		"yieldMs": float64(100),
		"timeout": float64(0.5),
	}, nil)
	require.NoError(t, err)
	id := extractSession(t, resultText(res))

	assert.Eventually(t, func() bool {
		out, perr := proc(t, p, map[string]any{"action": "poll", "session": id})
		return perr == nil && strings.Contains(out, "timed_out")
	}, 5*time.Second, 100*time.Millisecond, "hard timeout should kill the session")
}

func TestProcess_ScopeIsolation(t *testing.T) {
	dir := t.TempDir()
	reg := tool.NewSessionRegistry(tool.DefaultSessionTTL)
	b := tool.NewBashToolWith(func() string { return dir }, tool.NewLocalExecutor(), reg)
	b.ScopeFn = func() string { return "agent-a" }

	res, err := b.Execute(context.Background(), map[string]any{
		"command": "sleep 5",
		"yieldMs": float64(100),
	}, nil)
	require.NoError(t, err)
	id := extractSession(t, resultText(res))

	other := tool.NewProcessTool(reg, func() string { return "agent-b" })
	_, err = other.Execute(context.Background(), map[string]any{"action": "poll", "session": id}, nil)
	assert.Error(t, err, "session must be invisible from another scope")

	owner := tool.NewProcessTool(reg, func() string { return "agent-a" })
	_, err = owner.Execute(context.Background(), map[string]any{"action": "poll", "session": id}, nil)
	require.NoError(t, err)
	owner.Execute(context.Background(), map[string]any{"action": "kill", "session": id}, nil)
}
