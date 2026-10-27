package angelus

import (
	"log/slog"
	"sync"
	"time"
)

type heldNode struct {
	By    string
	Why   string
	Since time.Time
}

type heldNodes struct {
	mu  sync.Mutex
	now map[string]heldNode
}

func (h *heldNodes) keep(id, by, why, msg string, attrs ...any) {
	h.mu.Lock()
	if h.now == nil {
		h.now = map[string]heldNode{}
	}
	was, ok := h.now[id]
	changed := !ok || was.By != by || was.Why != why
	if changed {
		h.now[id] = heldNode{By: by, Why: why, Since: time.Now()}
	}
	h.mu.Unlock()
	if changed {
		slog.Info(msg, append([]any{"node", id, "held_by", by}, attrs...)...)
	}
}

func (h *heldNodes) release(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.now, id)
}

func (h *heldNodes) get(id string) (heldNode, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n, ok := h.now[id]
	return n, ok
}
