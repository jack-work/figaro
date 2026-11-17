package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type benchForkNoopProvider struct{}

func (benchForkNoopProvider) Name() string        { return "mock" }
func (benchForkNoopProvider) Fingerprint() string { return "mock/v0" }
func (benchForkNoopProvider) SetModel(string)     {}
func (benchForkNoopProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (benchForkNoopProvider) Send(context.Context, provider.SendInput, provider.Bus) error {
	return nil
}

type forkWakeFixture struct {
	backend  *store.XwalBackend
	angelus  *Angelus
	h        *handlers
	ctx      context.Context
	parentID string
	forkReq  json.RawMessage
}

func newForkWakeFixture(b *testing.B, trimmed bool) *forkWakeFixture {
	b.Helper()

	cfgDir := b.TempDir()
	if err := os.MkdirAll(filepath.Join(cfgDir, "outfits"), 0o700); err != nil {
		b.Fatal(err)
	}
	credo := strings.Repeat("Measure twice, cut once, leave no fingerprints. ", 120)
	outfitTOML := fmt.Sprintf("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\ncredo = %q\n", credo)
	if err := os.WriteFile(filepath.Join(cfgDir, "outfits", "mock.toml"), []byte(outfitTOML), 0o600); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("default_outfit = \"mock\"\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	loaded, err := config.Load(cfgDir)
	if err != nil {
		b.Fatal(err)
	}

	storeDir := filepath.Join(b.TempDir(), "arias")
	backend, err := store.NewXwalBackend(storeDir, loaded.SegmentSize())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = backend.Close() })

	rtDir := filepath.Join(b.TempDir(), "rt")
	a := New(Config{
		RuntimeDir: rtDir,
		SpoolDir:   filepath.Join(rtDir, "spool"),
		Backend:    backend,
		Settings:   loaded,
	})
	b.Cleanup(func() {
		for _, fg := range a.Registry.All() {
			_ = a.Registry.Kill(fg.ID())
		}
	})

	peer, err := authz.NewPeercred(uint32(os.Getuid()), loaded.Config.Authz.Peercred)
	if err != nil {
		b.Fatal(err)
	}
	a.Peercred = peer
	a.Policies = authz.NewPolicies(peer.Self(), StoreFacts(backend))
	if err := a.Policies.Reload(loaded.PolicyPath()); err != nil {
		b.Fatal(err)
	}

	baseCtx, cancel := context.WithCancel(context.Background())
	b.Cleanup(cancel)
	ctx := inProcess(authz.WithPrincipal(baseCtx, rpc.Principal{
		Kind: rpc.PrincipalUser,
		ID:   fmt.Sprint(os.Getuid()),
	}))

	hs := NewHandlers(ServerConfig{
		Angelus:       a,
		Config:        loaded,
		Ctx:           ctx,
		FormTemplates: template.New("bench"),
		ProviderFactory: func(name string, _ provider.Knobs) (provider.Provider, error) {
			return benchForkNoopProvider{}, nil
		},
	})
	a.Handlers = hs.Map
	a.RemoveAria = hs.RemoveAria
	a.RemoveAriaIf = hs.RemoveAriaIf

	createParams, _ := json.Marshal(rpc.CreateRequest{Outfits: []string{"mock"}})
	createdAny, err := hs.h.create(ctx, createParams)
	if err != nil {
		b.Fatal(err)
	}
	parentID := createdAny.(rpc.CreateResponse).FigaroID

	roleAPatch := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"role":        json.RawMessage(`"quartermaster"`),
		"target-aria": json.RawMessage(fmt.Sprintf("%q", parentID)),
		"status":      json.RawMessage(`"ready"`),
	}, nil)
	roleA, _, err := backend.CreateForm("", roleAPatch, store.MintedBy(fmt.Sprintf("user:%d", os.Getuid())))
	if err != nil {
		b.Fatal(err)
	}
	roleBPatch := form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"role":        json.RawMessage(`"sentinel"`),
		"target-aria": json.RawMessage(fmt.Sprintf("%q", parentID)),
		"status":      json.RawMessage(`"watching"`),
	}, nil)
	roleB, _, err := backend.CreateForm("", roleBPatch, store.MintedBy(fmt.Sprintf("user:%d", os.Getuid())))
	if err != nil {
		b.Fatal(err)
	}

	if _, err := backend.StudyForm(parentID, roleA, nil); err != nil {
		b.Fatal(err)
	}
	if _, err := backend.StudyForm(parentID, roleB, nil); err != nil {
		b.Fatal(err)
	}
	backend.SetObservedForms(parentID, []string{roleA, roleB})

	libA, err := backend.Libretto(roleA)
	if err != nil {
		b.Fatal(err)
	}
	syncRoleA := func(val string) {
		raw, _ := json.Marshal(val)
		v, err := backend.ApplyForm(roleA, form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"status": raw,
		}, nil))
		if err != nil {
			b.Fatal(err)
		}
		for libA.At() < v {
			// Wait on tmpfs fold so the libretto records distinct patches.
		}
	}

	irLog, err := backend.OpenFigIR(parentID)
	if err != nil {
		b.Fatal(err)
	}

	const turns = 250
	qText := strings.Repeat("Investigate the latency profile across storage and projection layers. ", 6)
	thinkText := strings.Repeat("Checking segment offsets, patch windows, and turn boundaries in detail. ", 18)
	toolOut := strings.Repeat("0x00001a2b  frame_ok  bytes=4096  elapsed_us=42  status=clean\n", 48)
	replyText := strings.Repeat("Verified the invariant across the active lineage and recorded measurements. ", 30)

	boundPatches := 0
	rolePatches := 0
	applyBound := func(turn int, step int) {
		raw, _ := json.Marshal(fmt.Sprintf("turn-%d-step-%d", turn, step))
		key := fmt.Sprintf("board.slot_%d", (turn+step)%16)
		if _, err := backend.ApplyForm(parentID, form.Build(form.Snapshot{}, map[string]json.RawMessage{
			key: raw,
		}, nil)); err != nil {
			b.Fatal(err)
		}
		boundPatches++
	}

	for t := 1; t <= turns; t++ {
		if trimmed {
			// Interleave >10 patches per turn so both early turns (versions < trimmed)
			// and late turns (versions > trimmed) carry form and libretto transitions,
			// pushing total patches past 2500 (> patchWindow 2048 + slack 256).
			for s := 0; s < 11; s++ {
				applyBound(t, s)
				rolePatches++
				syncRoleA(fmt.Sprintf("t%d-s%d", t, s))
			}
		} else if t%5 == 0 {
			applyBound(t, 0)
			rolePatches++
			syncRoleA(fmt.Sprintf("t%d", t))
		}

		tid := uint64(t)
		callID := fmt.Sprintf("call_%d", t)
		records := []message.Message{
			{
				Role:      message.RoleInput,
				TurnID:    tid,
				Timestamp: int64(1700000000000 + t*4000),
				Content:   []message.Content{message.TextContent(fmt.Sprintf("[turn %d] %s", t, qText))},
			},
			{
				Role:      message.RoleOutput,
				TurnID:    tid,
				Timestamp: int64(1700000000000 + t*4000 + 1000),
				Content: []message.Content{
					{Type: message.ContentThinking, Text: thinkText},
					{
						Type:       message.ContentToolInvoke,
						ToolCallID: callID,
						ToolName:   "bash",
						Arguments:  map[string]any{"command": "stat /tmp"},
					},
				},
				Usage: &message.Usage{InputTokens: 2000 + t*20, OutputTokens: 180, CacheReadTokens: 1500},
			},
			{
				Role:      message.RoleInput,
				TurnID:    tid,
				Timestamp: int64(1700000000000 + t*4000 + 2000),
				Content:   []message.Content{message.ToolResultContent(callID, "bash", toolOut, false)},
			},
			{
				Role:       message.RoleOutput,
				TurnID:     tid,
				Timestamp:  int64(1700000000000 + t*4000 + 3000),
				StopReason: message.StopEnd,
				Content:    []message.Content{message.TextContent(replyText)},
				Usage:      &message.Usage{InputTokens: 2800 + t*20, OutputTokens: 320, CacheReadTokens: 2200},
			},
		}
		for _, m := range records {
			if _, err := irLog.Append(arialog.Entry[message.Message]{Payload: m}); err != nil {
				b.Fatal(err)
			}
		}
	}

	if trimmed {
		bVer, err := backend.FormVersion(parentID)
		if err != nil {
			b.Fatal(err)
		}
		if bVer <= 2500 {
			b.Fatalf("bound form version = %d, want > 2500", bVer)
		}
		if libA.Version() <= 2500 {
			b.Fatalf("libretto version = %d, want > 2500", libA.Version())
		}
	}

	if _, err := hs.h.restoreByID(ctx, parentID, "bench-init"); err != nil {
		b.Fatal(err)
	}

	forkReq, _ := json.Marshal(rpc.ForkRequest{
		FigaroID: parentID,
		Patch: func() *rpc.FormPatch {
			p := form.Build(form.Snapshot{}, map[string]json.RawMessage{
				"mantra": json.RawMessage(`"forked worker"`),
			}, nil)
			return &p
		}(),
	})

	return &forkWakeFixture{
		backend:  backend,
		angelus:  a,
		h:        hs.h,
		ctx:      ctx,
		parentID: parentID,
		forkReq:  forkReq,
	}
}

// Case A: Live head-fork + new agent wake on a 250-turn (~1000 IR records, ~2MB)
// conversation with warm _meta and studied role forms.
func BenchmarkForkWake_LiveHead(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })

	fx := newForkWakeFixture(b, false)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		out, err := fx.h.fork(fx.ctx, fx.forkReq)
		if err != nil {
			b.Fatal(err)
		}
		alt := out.(rpc.ForkResponse).Alternative
		woke, err := fx.h.restoreByID(fx.ctx, alt, "qua")
		if err != nil {
			b.Fatal(err)
		}
		if woke.Info().MessageCount == 0 {
			b.Fatal("woke child lost message count")
		}
		b.StopTimer()
		if err := fx.angelus.Registry.Kill(alt); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

// Case B: Live head-fork + new agent wake when both the bound form and a studied
// libretto exceed 2500 patches (st.trimmed > 0, past patchWindow = 2048).
func BenchmarkForkWake_TrimmedWindow(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })

	fx := newForkWakeFixture(b, true)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		out, err := fx.h.fork(fx.ctx, fx.forkReq)
		if err != nil {
			b.Fatal(err)
		}
		alt := out.(rpc.ForkResponse).Alternative
		woke, err := fx.h.restoreByID(fx.ctx, alt, "qua")
		if err != nil {
			b.Fatal(err)
		}
		if woke.Info().MessageCount == 0 {
			b.Fatal("woke child lost message count")
		}
		b.StopTimer()
		if err := fx.angelus.Registry.Kill(alt); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

// Case C: Dormant wake + first UI ReadBefore (transcript attach) when opening
// a dormant 250-turn fork in the pager.
func BenchmarkForkWake_DormantReadBefore(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })

	fx := newForkWakeFixture(b, false)
	out, err := fx.h.fork(fx.ctx, fx.forkReq)
	if err != nil {
		b.Fatal(err)
	}
	forkID := out.(rpc.ForkResponse).Alternative
	if err := fx.angelus.Registry.Hibernate(fx.parentID); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		fx.backend.EvictIdle(nil, 0)
		fx.angelus.UICache = aria.NewComposedCache(uiBudget(fx.angelus.Settings), fx.angelus.composeTurns, fx.angelus.uiLineage)
		b.StartTimer()

		woke, err := fx.h.restoreByID(fx.ctx, forkID, "read")
		if err != nil {
			b.Fatal(err)
		}
		page := woke.(*figaro.Agent).ReadBefore(aria.Anchor{}, aria.Anchor{}, 64<<10)
		if len(page.Parts) == 0 || !page.More.Before {
			b.Fatalf("unexpected tail page: parts=%d more_before=%v", len(page.Parts), page.More.Before)
		}

		b.StopTimer()
		if err := fx.angelus.Registry.Hibernate(forkID); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

// Case D: Full transcript hydration (Read from head) on a trimmed-window (>2500
// patches on bound form and studied libretto) 250-turn fork, isolating Fix 1
// (PerRecordFrom + Attach) even after wake itself becomes lazy.
func BenchmarkForkWake_TrimmedHydrate(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(old) })

	fx := newForkWakeFixture(b, true)
	out, err := fx.h.fork(fx.ctx, fx.forkReq)
	if err != nil {
		b.Fatal(err)
	}
	forkID := out.(rpc.ForkResponse).Alternative
	if err := fx.angelus.Registry.Hibernate(fx.parentID); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		woke, err := fx.h.restoreByID(fx.ctx, forkID, "read")
		if err != nil {
			b.Fatal(err)
		}
		page := woke.(*figaro.Agent).Read(aria.Anchor{}, 64<<10)
		if len(page.Parts) == 0 || !page.More.After {
			b.Fatalf("unexpected head page: parts=%d more_after=%v", len(page.Parts), page.More.After)
		}

		b.StopTimer()
		if err := fx.angelus.Registry.Hibernate(forkID); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}
