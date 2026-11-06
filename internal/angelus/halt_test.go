package angelus

import (
	"context"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
)

type haltRecorder struct {
	id     string
	halted bool
}

func (h *haltRecorder) ID() string { return h.id }
func (h *haltRecorder) Suspend()   {}
func (h *haltRecorder) Halt() bool { h.halted = true; return true }
func (h *haltRecorder) Kill()      {}
func (h *haltRecorder) Info() figaro.FigaroInfo {
	return figaro.FigaroInfo{ID: h.id, State: figaro.StateSuspended}
}

func haltHandlers(t *testing.T, stop func()) (*handlers, *haltRecorder) {
	t.Helper()
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t), StopSelf: stop}
	rec := &haltRecorder{id: "0123456789abcdef0123456789abcdef"}
	if err := a.Registry.Register(rec); err != nil {
		t.Fatal(err)
	}
	return NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: inProcess(context.Background())}).h, rec
}

// A halt that is not followed by a stop would leave every aria suspended in a
// daemon that keeps running, accepting sends and starting no turn. The halt
// and the stop are one act: the daemon stops itself once its turns are hung up.
func TestHaltAndStopStopsTheDaemonItHalted(t *testing.T) {
	stopped := make(chan struct{})
	h, rec := haltHandlers(t, func() { close(stopped) })
	if _, err := h.haltAndStop(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !rec.halted {
		t.Fatal("the turn was not halted")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon halted its turns but never began to stop")
	}
}

// A daemon that cannot stop itself must not halt anything: that is the wedge.
func TestHaltAndStopRefusesWithoutAWayToStop(t *testing.T) {
	h, rec := haltHandlers(t, nil)
	if _, err := h.haltAndStop(context.Background(), nil); err == nil {
		t.Fatal("a daemon with no way to stop itself accepted a halt")
	}
	if rec.halted {
		t.Fatal("a refused halt still halted a turn")
	}
}
