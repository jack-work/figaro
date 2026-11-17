package angelus

import (
	"context"
	"os/exec"
	"testing"
	"time"

	fwtree "github.com/jack-work/figaro/figwal/tree"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
)

type stuckBudgets struct {
	store.Backend
	release chan struct{}
}

func (s stuckBudgets) SweepCacheBudgets() (int, int64) {
	<-s.release
	return 0, 0
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("cannot run true:", err)
	}
	return cmd.Process.Pid
}

// Defends #130: a cache budget sweep that runs long must not hold up reaping
// the bindings of dead shells.
func TestSlowCacheSweepDoesNotStallPIDReaping(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	a := &Angelus{Registry: NewRegistry(), Backend: stuckBudgets{release: release}}
	a.UICache = aria.NewComposedCache(fwtree.NewBudget(1<<20), a.composeTurns, a.uiLineage)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a.startSweeps(ctx)

	time.Sleep(2500 * time.Millisecond)
	pid := deadPID(t)
	if err := a.Registry.Bind(pid, "f", 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if id, _, _ := a.Registry.Resolve(pid); id == "" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("a dead pid stayed bound while a cache budget sweep was running")
}
