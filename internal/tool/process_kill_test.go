package tool_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/sandbox/local"
	"github.com/jack-work/figaro/internal/tool"
)

type deafProc struct{ done chan struct{} }

func (d *deafProc) Wait() sandbox.Result        { <-d.done; return sandbox.Result{} }
func (d *deafProc) Done() <-chan struct{}       { return d.done }
func (d *deafProc) Write(p []byte) (int, error) { return len(p), nil }
func (d *deafProc) Interrupt() error            { return nil }
func (d *deafProc) Kill() error                 { return nil }
func (d *deafProc) PTY() bool                   { return false }

// Issue #42: kill blocked on Done with no bound, so a session whose exit never
// arrived hung the tool call forever.
func TestProcess_KillReturnsWhenExitNeverConfirms(t *testing.T) {
	orig := tool.KillConfirmWait
	tool.KillConfirmWait = 200 * time.Millisecond
	t.Cleanup(func() { tool.KillConfirmWait = orig })

	proc := &deafProc{done: make(chan struct{})}
	t.Cleanup(func() { close(proc.done) })
	sessions := local.NewHost(local.DefaultTTL).Scoped("s", func(sandbox.ExecRequest) (sandbox.Process, error) { return proc, nil })
	sess, err := sessions.Start(sandbox.ExecRequest{Command: "sleep forever"})
	require.NoError(t, err)

	p := &tool.ProcessTool{Sessions: sessions}
	finished := make(chan string, 1)
	go func() {
		res, err := p.Execute(context.Background(), map[string]any{"action": "kill", "session": sess.ID()}, nil)
		require.NoError(t, err)
		finished <- resultText(res)
	}()
	select {
	case out := <-finished:
		assert.Contains(t, out, "[killing]")
		assert.Contains(t, out, "not confirmed")
	case <-time.After(5 * time.Second):
		t.Fatal("kill still blocked on a session whose exit never arrives")
	}
}
