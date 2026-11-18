package figaro_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
)

// A send while the disk stalls: the spool write that makes a queued prompt
// survive a restart must not hold the sender, or the pager's queue send hits
// its deadline behind an fsync.
func TestAQueuedPromptDoesNotWaitForTheSpoolWrite(t *testing.T) {
	be, id := backedConv(t, t.TempDir())
	dir := filepath.Join(t.TempDir(), "spool")
	spool := figaro.NewSpool(dir)
	gate := make(chan struct{})
	figaro.HoldSpoolWrites(spool, gate)

	stubborn := &stubbornProvider{started: make(chan struct{}), release: make(chan struct{})}
	a := spoolAgent(t, be, id, spool, stubborn)
	released := false
	release := func() {
		if !released {
			released = true
			close(gate)
		}
	}
	t.Cleanup(func() {
		release()
		close(stubborn.release)
		a.Kill()
	})

	sent := make(chan struct{})
	go func() {
		a.SubmitPrompt(rpc.QuaRequest{Text: "first"})
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("a send waited for the spool write")
	}
	select {
	case <-stubborn.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first turn never reached the provider")
	}
	done := make(chan struct{})
	go func() {
		a.SubmitPrompt(rpc.QuaRequest{Text: "queued behind a stalled disk"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a send waited for the spool write")
	}

	release()
	a.FlushSpool()
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	require.NoError(t, err)
	require.Contains(t, string(data), "queued behind a stalled disk", "the spool lands once the disk answers")
}
