package angelus

import (
	"context"
	"fmt"
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
	kind, err := h.boxOf(id)
	if err != nil {
		slog.Warn("sessions in a box may outlive the aria: its box cannot be read", "id", id, "err", err)
		return n
	}
	loaded, _ := h.settings()
	if _, ok := loaded.Box(kind); !ok {
		return n
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := sandbox.KillBoxScope(ctx, loaded, kind, id)
	if err != nil {
		slog.Warn("sessions in the box may outlive the aria", "id", id, "box", kind, "err", err)
	}
	return n + m
}

func (h *handlers) refuseUnreadableBoxes(ids []string) error {
	for _, id := range ids {
		if _, err := h.boxOf(id); err != nil {
			return fmt.Errorf("the box of %s cannot be read, so its sessions could not go with it: %w", id, err)
		}
	}
	return nil
}

func (h *handlers) sandboxOf(id string) string {
	box, _ := h.boxOf(id)
	return box
}

func (h *handlers) boxOf(id string) (string, error) {
	if h.angelus.Backend == nil {
		return "", nil
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return "", err
	}
	if v := snap.Lookup("system.sandbox"); v != nil {
		return *v, nil
	}
	return "", nil
}
