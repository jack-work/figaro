package angelus

// Waking a dormant aria: the single-flight, and what a restored agent is
// constructed from.
//
// Split out of protocol.go, which had grown to 2,011 lines and answered every
// question at once. Same package, same behaviour: only the reader's job
// changes. plans/api-coherence.md step 5.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/figaro"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// restoreCall is one wake, shared by everyone who asked for it while it ran.
type restoreCall struct {
	done chan struct{}
	f    figaro.Figaro
	err  error
}

// restoreByID re-creates a figaro from the backend tree. Serialized per
// aria so concurrent restores cannot double-replay tail repair.
func (h *handlers) restoreByID(ctx context.Context, ariaID string) (figaro.Figaro, error) {
	if f := h.angelus.Registry.Get(ariaID); f != nil {
		return f, nil
	}
	// SINGLE FLIGHT. Two requests for a dormant aria must not build two
	// agents, and the second must not wait on a lock that outlives the wake.
	h.restoreMu.Lock()
	if call, ok := h.restoring[ariaID]; ok {
		h.restoreMu.Unlock()
		select {
		case <-call.done:
			// The winner registered it; prefer the registry, which is the
			// truth, and fall back to what the call returned.
			if f := h.angelus.Registry.Get(ariaID); f != nil {
				return f, nil
			}
			return call.f, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &restoreCall{done: make(chan struct{})}
	if h.restoring == nil {
		h.restoring = map[string]*restoreCall{}
	}
	h.restoring[ariaID] = call
	h.restoreMu.Unlock()

	if f := h.angelus.Registry.Get(ariaID); f != nil {
		call.f = f
	} else {
		call.f, call.err = h.restoreOne(ctx, ariaID)
	}
	// The entry goes as the wake ends, which is what makes this bounded by
	// concurrent wakes rather than by arias ever woken.
	h.restoreMu.Lock()
	delete(h.restoring, ariaID)
	h.restoreMu.Unlock()
	close(call.done)
	return call.f, call.err
}

// restoreOne builds and registers a figaro for an existing conversation
// node, seeding its form from the channel.
func (h *handlers) restoreOne(ctx context.Context, ariaID string) (figaro.Figaro, error) {
	cb := h.openAriaForm(ariaID)
	if cb == nil {
		return nil, fmt.Errorf("restore %s: form unavailable", ariaID)
	}
	cbSnap := cb.Snapshot()
	cbStr := func(key string) string {
		raw, ok := cbSnap.Get(key)
		if !ok {
			return ""
		}
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	cbInt := func(key string) int {
		raw, ok := cbSnap.Get(key)
		if !ok {
			return 0
		}
		var n int
		_ = json.Unmarshal(raw, &n)
		return n
	}
	cbBool := func(key string) bool {
		raw, ok := cbSnap.Get(key)
		if !ok {
			return false
		}
		var b bool
		_ = json.Unmarshal(raw, &b)
		return b
	}
	provName := cbStr("system.provider")
	knobs := providerPkg.Knobs{
		Model:            cbStr("system.model"),
		MaxTokens:        cbInt("system.max_tokens"),
		ReminderRenderer: cbStr("system.reminder_renderer"),
		UseOfficialSDK:   cbBool("system.use_official_sdk"),
	}
	cwd := cbStr("system.cwd")

	prov, err := h.factory(provName, knobs)
	if err != nil {
		return nil, fmt.Errorf("restore %s: create provider: %w", ariaID, err)
	}

	sockPath := filepath.Join(h.angelus.FigaroSocketDir(), ariaID+".sock")

	// Fall back if restored cwd no longer exists.
	toolRoot := cwd
	if _, err := os.Stat(toolRoot); err != nil {
		toolRoot, _ = os.Getwd()
	}

	var createdAt, lastActive time.Time
	if meta, _ := h.angelus.Backend.Meta(ariaID); meta != nil {
		if meta.CreatedAtMS != 0 {
			createdAt = time.UnixMilli(meta.CreatedAtMS)
		}
	}
	if ts := h.angelus.Backend.LastTS(ariaID); ts != 0 {
		lastActive = time.UnixMilli(ts)
	}
	loaded, _ := h.settings()
	reg := tool.DefaultRegistryForAria(ariaID, cwdFromForm(cb, toolRoot),
		tool.WithImageBudget(loaded.InlineImageBudget()),
		tool.WithSessions(h.angelus.Sessions))
	agent := figaro.NewAgent(figaro.Config{
		ID:              ariaID,
		SocketPath:      sockPath,
		Provider:        prov,
		ProviderFactory: h.factory,
		Tools:           reg,
		Projector:       uiir.New(reg),
		Backend:         h.angelus.Backend,
		Form:            cb,
		CreatedAt:       createdAt,
		LastActive:      lastActive,
		Settings:        loaded,
		UICache:         h.angelus.UICache,
	})

	if err := h.angelus.Registry.Register(agent); err != nil {
		agent.Kill()
		return nil, fmt.Errorf("restore %s: register: %w", ariaID, err)
	}

	unbind, herr := h.bindAgentToHub(ariaID, agent)
	if herr != nil {
		h.angelus.Registry.Kill(ariaID)
		return nil, fmt.Errorf("restore %s: open endpoint: %w", ariaID, herr)
	}
	agent.OnTeardown(unbind)

	slog.Info("restored figaro",
		"id", ariaID, "provider", provName, "model", knobs.Model)
	return agent, nil
}

// cwdFromForm returns a closure that reads system.cwd from
// cbState at call time, falling back to fallback when the key is
// unset, the form is nil, or the value isn't a JSON string.
func cwdFromForm(cbState *form.State, fallback string) func() string {
	return func() string {
		if cbState == nil {
			return fallback
		}
		if s := cbState.Snapshot().Lookup("system.cwd"); s != nil && *s != "" {
			return *s
		}
		return fallback
	}
}
