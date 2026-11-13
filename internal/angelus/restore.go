package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/figaro"
	figOtel "github.com/jack-work/figaro/internal/otel"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/sandbox"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

type restoreCall struct {
	done chan struct{}
	f    figaro.Figaro
	err  error
}

// restoreByID is single-flight per aria so concurrent restores cannot
// double-replay tail repair.
func (h *handlers) restoreByID(ctx context.Context, ariaID, wokeBy string) (figaro.Figaro, error) {
	if f := h.angelus.Registry.Get(ariaID); f != nil {
		return f, nil
	}
	if b, ok := h.angelus.Backend.(interface{ Buried(string) bool }); ok && b.Buried(ariaID) {
		return nil, fmt.Errorf("%s was deleted: its form carries a tombstone", ariaID)
	}
	done, err := h.angelus.guard.enter(ariaID)
	if err != nil {
		return nil, err
	}
	defer done()
	h.restoreMu.Lock()
	if h.releasing[ariaID] {
		h.restoreMu.Unlock()
		return nil, fmt.Errorf("%s is being released from quarantine; try again when that is done", ariaID)
	}
	if err := h.holdsMove(ariaID); err != nil {
		h.restoreMu.Unlock()
		return nil, err
	}
	if call, ok := h.restoring[ariaID]; ok {
		h.restoreMu.Unlock()
		select {
		case <-call.done:
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
		woke := time.Now()
		call.f, call.err = h.restoreOne(ctx, ariaID, wokeBy)
		figOtel.RecordWake(ctx, time.Since(woke), attribute.String("woke_by", wokeBy), attribute.Bool("ok", call.err == nil))
	}
	h.restoreMu.Lock()
	delete(h.restoring, ariaID)
	h.restoreMu.Unlock()
	close(call.done)
	return call.f, call.err
}

func (h *handlers) restoreOne(ctx context.Context, ariaID, wokeBy string) (figaro.Figaro, error) {
	if b := h.angelus.Backend; b != nil {
		if _, err := b.OpenFigIR(ariaID); err != nil {
			return nil, fmt.Errorf("restore %s: %w", ariaID, err)
		}
	}
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
	provName := cbStr("system.provider")
	knobs := providerPkg.Knobs{
		Model:            cbStr("system.model"),
		MaxTokens:        cbInt("system.max_tokens"),
		ReminderRenderer: cbStr("system.reminder_renderer"),
	}
	prov, err := h.factoryIn(cbStr("system.sandbox"))(provName, knobs)
	if err != nil {
		return nil, fmt.Errorf("restore %s: create provider: %w", ariaID, err)
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
	if err := h.mayRunIn(cbStr("system.sandbox"), ariaID); err != nil {
		return nil, fmt.Errorf("restore %s: sandbox: %w", ariaID, err)
	}
	box, err := sandbox.Open(ctx, h.angelus.Sessions, loaded, cbStr("system.sandbox"), ariaID, cbStr("system.cwd"))
	if err != nil {
		return nil, fmt.Errorf("restore %s: sandbox: %w", ariaID, err)
	}
	reg := h.toolsIn(cbStr("system.sandbox"), ariaID, func() *tool.Registry {
		return tool.DefaultRegistry(box, cwdFromForm(cb, box), tool.WithImageBudget(loaded.InlineImageBudget()))
	})
	agent := figaro.NewAgent(figaro.Config{
		ID:              ariaID,
		Provider:        prov,
		ProviderFactory: h.factoryIn(cbStr("system.sandbox")),
		Models:          h.angelus.Models,
		Tools:           reg,
		Projector:       uiir.New(reg).Naming(h.angelus.senderName),
		Backend:         h.angelus.Backend,
		Form:            cb,
		CreatedAt:       createdAt,
		LastActive:      lastActive,
		WokeBy:          wokeBy,
		Settings:        loaded,
		UICache:         h.angelus.UICache,
		Decider:         h.methodAsker(),
		DefaultForm:     h.defaultForm,
		Spool:           h.angelus.Spool,
		TellFailure:     h.tellFailure,
		TurnEnded:       h.turnEnded,
	})

	if err := h.angelus.Registry.Register(agent); err != nil {
		agent.Kill()
		box.Close()
		return nil, fmt.Errorf("restore %s: register: %w", ariaID, err)
	}

	agent.OnTeardown(h.bindAgentToHub(ariaID, agent))
	agent.OnTeardown(func() { box.Close() })
	agent.Redeliver()

	slog.Info("restored figaro",
		"id", ariaID, "provider", provName, "model", knobs.Model, "woke_by", wokeBy)
	return agent, nil
}

func cwdFromForm(cbState *form.State, box sandbox.Opened) func() string {
	return func() string {
		if cbState != nil {
			if s := cbState.Snapshot().Lookup("system.cwd"); s != nil && *s != "" {
				if info, err := box.FS().Stat(*s); err == nil && info.IsDir() {
					return *s
				}
				slog.Error("system.cwd is not usable in the sandbox, falling back to the workspace", "cwd", *s)
			}
		}
		return box.Workspace
	}
}
