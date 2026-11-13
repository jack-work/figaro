package angelus

import (
	"context"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/sandbox"
)

func (h *handlers) dropHostCwdInBox(id string, dressedCwd bool) error {
	if dressedCwd {
		return nil
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return err
	}
	kind := ""
	if v := snap.Lookup("system.sandbox"); v != nil {
		kind = *v
	}
	loaded, _ := h.settings()
	if _, boxed := loaded.Box(kind); !boxed {
		return nil
	}
	if v := snap.Lookup("system.cwd"); v == nil || *v == "" {
		return nil
	}
	_, err = h.angelus.Backend.ApplyFormPrivileged(id, form.Build(form.Snapshot{}, nil, []string{"system.cwd"}))
	return err
}

func (h *handlers) killSessions(id string) int {
	n := 0
	if h.angelus.Sessions != nil {
		n = h.angelus.Sessions.KillScope(id)
	}
	kind := h.sandboxOf(id)
	loaded, _ := h.settings()
	if _, ok := loaded.Box(kind); !ok {
		h.leaveLeasedSandbox(id, kind)
		return n
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := sandbox.KillBoxScope(ctx, loaded, kind, id)
	if err != nil {
		slog.Warn("sessions in the box may outlive the aria", "id", id, "box", kind, "err", err)
	}
	h.leaveLeasedSandbox(id, kind)
	return n + m
}

func (h *handlers) sandboxOf(id string) string {
	if h.angelus.Backend == nil {
		return ""
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return ""
	}
	if v := snap.Lookup("system.sandbox"); v != nil {
		return *v
	}
	return ""
}
