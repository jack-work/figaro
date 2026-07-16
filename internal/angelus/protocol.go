package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"text/template"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/outfit"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figwal/segment"
	"github.com/jack-work/jkrpc"
)

// ProviderFactory creates a provider from a name and operational knobs.
type ProviderFactory func(providerName string, knobs providerPkg.Knobs) (providerPkg.Provider, error)

// ServerConfig holds dependencies for the angelus JSON-RPC handlers.
type ServerConfig struct {
	Angelus         *Angelus
	Config          *config.Loaded
	ProviderFactory ProviderFactory
	Ctx             context.Context

	// AvailableProviders is the list of provider names the factory
	// knows how to construct. Surfaced in typed JSON-RPC errors so
	// clients can drive first-run provider selection.
	AvailableProviders []string

	// ChalkboardTemplates renders Patches as system reminders. nil = skip.
	ChalkboardTemplates *template.Template
}

// Handlers wraps the angelus JSON-RPC handler map.
type Handlers struct {
	Map map[string]jkrpc.HandlerFunc
	h   *handlers
}

// NewHandlers creates the handler set for the angelus socket.
func NewHandlers(cfg ServerConfig) *Handlers {
	h := &handlers{
		angelus:            cfg.Angelus,
		config:             cfg.Config,
		factory:            cfg.ProviderFactory,
		ctx:                cfg.Ctx,
		cbTmpls:            cfg.ChalkboardTemplates,
		outfitter:          outfit.New(cfg.Config.ConfigDir),
		availableProviders: cfg.AvailableProviders,
	}
	return &Handlers{
		Map: map[string]jkrpc.HandlerFunc{
			rpc.MethodCreate:       h.create,
			rpc.MethodFork:         h.fork,
			rpc.MethodPromote:      h.promote,
			rpc.MethodKill:         h.kill,
			rpc.MethodList:         h.list,
			rpc.MethodAttach:       h.attach,
			rpc.MethodBind:         h.bind,
			rpc.MethodResolve:      h.resolve,
			rpc.MethodUnbind:       h.unbind,
			rpc.MethodStatus:       h.status,
			rpc.MethodSaveBindings: h.saveBindings,
			rpc.MethodAriaRead:     h.ariaRead,
		},
		h: h,
	}
}

// Restore lazily re-creates the agent for ariaID.
func (hs *Handlers) Restore(ctx context.Context, ariaID string) (figaro.Figaro, error) {
	return hs.h.restoreByID(ctx, ariaID)
}

type handlers struct {
	angelus            *Angelus
	config             *config.Loaded
	factory            ProviderFactory
	ctx                context.Context
	cbTmpls            *template.Template
	outfitter          *outfit.Outfitter
	availableProviders []string

	// configMu guards config against concurrent reload + read. The
	// reload-from-disk is cheap, but other handlers may dereference
	// h.config concurrently.
	configMu sync.Mutex

	// loadoutHashCache memoizes currentLoadoutHash with a short TTL. List
	// calls it once per aria, but loadout content is shared and expensive to
	// hash (outfitter.Load re-reads every skill file), so without this an
	// 8-aria list re-read all skills 8× (~0.5s) and starved completion's
	// short-timeout List call.
	loadoutHashMu    sync.Mutex
	loadoutHashCache map[string]loadoutHashEntry
}

type loadoutHashEntry struct {
	hash string
	at   time.Time
}

// reloadConfigIfChanged re-reads config.toml from disk when the
// in-memory copy looks stale relative to a wizard write. We're
// conservative: only reload when the in-memory DefaultLoadout is
// empty AND a config.toml exists on disk. This means tests that
// inject loaded.Config.DefaultLoadout in memory without a backing
// file are untouched, while the production case (first-run wizard
// writes config.toml + a loadout, then retries Create) sees the
// fresh value.
func (h *handlers) reloadConfigIfChanged() {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	if h.config.Config.DefaultLoadout != "" {
		return // already have one in memory; nothing the wizard could change
	}
	if _, err := os.Stat(h.config.ConfigPath); err != nil {
		return // no file on disk; can't possibly have new state
	}
	fresh, err := config.Load(h.config.ConfigDir)
	if err != nil {
		return
	}
	h.config = fresh
	h.outfitter = outfit.New(fresh.ConfigDir)
}

// openAriaChalkboard returns the in-memory chalkboard hot view for an
// aria, seeded from its reducible chalkboard channel (the durable
// truth — there is no chalkboard.json). nil on failure.
func (h *handlers) openAriaChalkboard(ariaID string) *chalkboard.State {
	if h.cbTmpls == nil || h.angelus.Backend == nil {
		return nil
	}
	snap, err := h.angelus.Backend.ChalkboardState(ariaID)
	if err != nil {
		slog.Warn("chalkboard state (disabled for aria)", "aria", ariaID, "err", err)
		return nil
	}
	st, _ := chalkboard.Open("")
	if len(snap) > 0 {
		st.Apply(chalkboard.Patch{Set: snap})
	}
	return st
}

// fillFromChalkboard fills Provider/Model/Mantra/Cwd from the aria's
// chalkboard channel state.
func (h *handlers) fillFromChalkboard(ariaID string, entry *rpc.FigaroInfoResponse) {
	if h.angelus.Backend == nil {
		return
	}
	snap, err := h.angelus.Backend.ChalkboardState(ariaID)
	if err != nil {
		return
	}
	get := func(key string) string {
		if s := snap.Lookup(key); s != nil {
			return *s
		}
		return ""
	}
	if entry.Provider == "" {
		entry.Provider = get("system.provider")
	}
	if entry.Model == "" {
		entry.Model = get("system.model")
	}
	if entry.Mantra == "" {
		entry.Mantra = get("mantra")
	}
	if entry.Cwd == "" {
		entry.Cwd = get("system.cwd")
	}
	// Loadout: the name + whether the conversation's stamped content hash is
	// still the current one ("live") or an older version (its short hash).
	if name := get("system.loadout_name"); name != "" {
		entry.LoadoutName = name
		stamped := get("system.loadout_version")
		entry.LoadoutVer = loadoutVerLabel(stamped, h.currentLoadoutHash(name))
	}
}

// currentLoadoutHash is the content hash the loadout would have right now
// (recomputed from the on-disk definition), or "" if it can't be loaded.
func (h *handlers) currentLoadoutHash(name string) string {
	if h.outfitter == nil {
		return ""
	}
	h.loadoutHashMu.Lock()
	if h.loadoutHashCache == nil {
		h.loadoutHashCache = map[string]loadoutHashEntry{}
	}
	if e, ok := h.loadoutHashCache[name]; ok && time.Since(e.at) < 3*time.Second {
		h.loadoutHashMu.Unlock()
		return e.hash
	}
	h.loadoutHashMu.Unlock()

	hash := ""
	if p, err := h.outfitter.Load(name); err == nil {
		if body, merr := json.Marshal(p); merr == nil {
			hash, _ = segment.ValueHash(body)
		}
	}
	h.loadoutHashMu.Lock()
	h.loadoutHashCache[name] = loadoutHashEntry{hash: hash, at: time.Now()}
	h.loadoutHashMu.Unlock()
	return hash
}

// loadoutVerLabel renders the version column: "live" when the stamped hash
// matches the current one, else the stamped hash's first 8 chars.
func loadoutVerLabel(stamped, current string) string {
	if stamped == "" {
		return ""
	}
	if current != "" && stamped == current {
		return "live"
	}
	if len(stamped) > 8 {
		return stamped[:8]
	}
	return stamped
}

// openAriaTranslation opens the per-aria translation cache. nil on failure.
func (h *handlers) openAriaTranslation(ariaID, providerName string) store.Log[[]json.RawMessage] {
	if h.angelus.Backend == nil {
		return nil
	}
	stream, err := h.angelus.Backend.OpenTranslation(ariaID, providerName)
	if err != nil {
		slog.Warn("translator stream open (cache disabled for aria)", "aria", ariaID, "provider", providerName, "err", err)
		return nil
	}
	return stream
}

func (h *handlers) create(ctx context.Context, params json.RawMessage) (interface{}, error) {
	_, span := figOtel.Start(ctx, "angelus.create")
	defer span.End()

	var req rpc.CreateRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}

	// Resolve the loadout name. Empty request → configured default →
	// typed JSON-RPC error so the client can drive first-run setup.
	//
	// We re-read config.toml from disk first so that wizard-driven
	// changes (the first-run flow scaffolds a loadout + sets
	// default_loadout, then retries this Create call) are picked up
	// without a daemon restart. One os.ReadFile + toml.Unmarshal per
	// request is cheap relative to anything downstream.
	h.reloadConfigIfChanged()
	loadoutName := req.Loadout
	if loadoutName == "" {
		loadoutName = h.config.Config.DefaultLoadout
	}
	if loadoutName == "" {
		return nil, h.errNoDefaultLoadout()
	}

	// Resolve loadout -> chalkboard patch. Missing files are not
	// fatal; the patch comes back empty and req.Patch may still
	// supply system.provider. loadoutPatch is the STABLE loadout (it
	// defines the loadout node's identity/version); base layers the
	// per-create req.Patch overrides on top for provider/knob resolution.
	loadoutPatch, err := h.outfitter.Load(loadoutName)
	if err != nil {
		return nil, h.errLoadoutNotFound(loadoutName, err)
	}
	base := chalkboard.Patch{Set: map[string]json.RawMessage{}}
	for k, v := range loadoutPatch.Set {
		base.Set[k] = v
	}
	base.Remove = append(base.Remove, loadoutPatch.Remove...)
	if req.Patch != nil {
		for k, v := range req.Patch.Set {
			base.Set[k] = v
		}
		base.Remove = append(base.Remove, req.Patch.Remove...)
	}

	provName := patchString(base, "system.provider")
	if provName == "" {
		return nil, h.errNoProvider(loadoutName)
	}
	knobs := knobsFromPatch(base)

	span.SetAttributes(
		attribute.String("figaro.loadout", loadoutName),
		attribute.String("figaro.provider", provName),
		attribute.String("figaro.model", knobs.Model),
	)

	prov, err := h.factory(provName, knobs)
	if err != nil {
		return nil, fmt.Errorf("create provider %q: %w", provName, err)
	}

	cwd, _ := os.Getwd()

	// Ephemeral: in-memory only, no tree.
	backend := h.angelus.Backend
	if req.Ephemeral {
		backend = nil
	}

	// The chalkboard channel is the durable truth; cbState is the
	// in-memory hot view (no chalkboard.json). System mints all ids.
	cbState, _ := chalkboard.Open("")
	var id string
	var inlineBoot *chalkboard.Patch

	if backend == nil {
		// Ephemeral: no channel. Seed state with the full loadout +
		// runtime fill-ins, and fold the same patch on the first message so
		// reminders render.
		id = uuid.New().String()[:8]
		boot := bootPatchEphemeral(base, "", cwd) // id filled below
		boot = withAriaID(boot, id)
		cbState.Apply(boot)
		bp := boot
		inlineBoot = &bp
	} else {
		// Materialize/reuse the loadout node (identity = stable loadout
		// patch), fork it into a fresh conversation, then write the
		// per-conversation boot transition (runtime fill-ins + req.Patch
		// overrides) to its chalkboard channel. The loadout's own
		// reminders render in the shared loadout-node prefix.
		loadoutID, lerr := backend.CreateLoadout(loadoutName, loadoutPatch)
		if lerr != nil {
			return nil, fmt.Errorf("create loadout node: %w", lerr)
		}
		var cerr error
		id, cerr = backend.CreateConversation(loadoutID)
		if cerr != nil {
			return nil, fmt.Errorf("create conversation: %w", cerr)
		}
		boot := convBootPatch(req.Patch, id, cwd)
		if !boot.IsEmpty() {
			if aerr := backend.ApplyChalkboard(id, boot); aerr != nil {
				return nil, fmt.Errorf("seed conversation chalkboard: %w", aerr)
			}
		}
		snap, serr := backend.ChalkboardState(id)
		if serr != nil {
			return nil, fmt.Errorf("read conversation chalkboard: %w", serr)
		}
		cbState.Apply(chalkboard.Patch{Set: snap})
	}

	sockPath := filepath.Join(h.angelus.FigaroSocketDir(), id+".sock")

	agent := figaro.NewAgent(figaro.Config{
		ID:         id,
		SocketPath: sockPath,
		Provider:   prov,
		Outfitter:  h.outfitter,
		Tools:      tool.DefaultRegistryFn(cwdFromChalkboard(cbState, cwd)),
		Backend:    backend,
		Chalkboard: cbState,
		InlineBoot: inlineBoot,
	})

	if err := h.angelus.Registry.Register(agent); err != nil {
		agent.Kill()
		return nil, err
	}

	go agent.StartSocket(h.ctx)

	slog.Info("created figaro",
		"id", id, "loadout", loadoutName, "provider", provName, "model", knobs.Model, "socket", sockPath)

	return rpc.CreateResponse{
		FigaroID: id,
		Endpoint: rpc.Endpoint{
			Scheme:  "unix",
			Address: sockPath,
		},
	}, nil
}

// fork branches a conversation at its head. The live agent (if any) is
// killed first — the node freezes and keeps its id as a read-only index
// node — and both fresh children become dormant conversations the
// caller can attach to.
func (h *handlers) fork(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ForkRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if h.angelus.Backend == nil {
		return nil, errors.New("fork: no backend (ephemeral angelus)")
	}
	node, ok := h.angelus.Backend.Node(req.FigaroID)
	if !ok {
		return nil, fmt.Errorf("fork: no aria %q", req.FigaroID)
	}
	if node.Kind != "conversation" {
		return nil, fmt.Errorf("fork: %q is a %s node, not a conversation", req.FigaroID, node.Kind)
	}
	// Stop the live agent so it releases the node before it freezes.
	if h.angelus.Registry.Get(req.FigaroID) != nil {
		if err := h.angelus.Registry.Kill(req.FigaroID); err != nil {
			return nil, fmt.Errorf("fork: kill live agent: %w", err)
		}
	}
	// Announce when an interior <id>:<LT> resolves to an owning ancestor —
	// the LT lives in a parent trunk / loadout / the genesis root, so the
	// branch is made there, not in this trunk's own range.
	note := ""
	if req.AtMainLT > 0 {
		if o, oerr := h.angelus.Backend.OwnerResolution(req.FigaroID, req.AtMainLT); oerr == nil {
			switch {
			case o.IsRoot:
				note = fmt.Sprintf("LT %d is the genesis root — spawned a fresh loadoutless conversation there", req.AtMainLT)
			case o.Loadout != "":
				note = fmt.Sprintf("LT %d is in loadout %s — spawned a fresh conversation under it", req.AtMainLT, o.Loadout)
			case o.Trunk != "" && o.Trunk != req.FigaroID:
				note = fmt.Sprintf("LT %d lives in trunk %s — branching there", req.AtMainLT, o.Trunk)
			}
		}
	}

	var cont, alt string
	var err error
	if req.AtMainLT > 0 {
		cont, alt, err = h.angelus.Backend.ForkAt(req.FigaroID, req.AtMainLT)
	} else {
		cont, alt, err = h.angelus.Backend.Fork(req.FigaroID)
	}
	if err != nil {
		return nil, fmt.Errorf("fork %q: %w", req.FigaroID, err)
	}
	slog.Info("forked figaro", "parent", req.FigaroID, "at", req.AtMainLT, "continuation", cont, "alternative", alt)
	return rpc.ForkResponse{Parent: req.FigaroID, Continuation: cont, Alternative: alt, OwnerNote: note}, nil
}

// promote climbs a conversation trunk up N stump-bounded levels (it absorbs
// its parent trunk's run). A live agent on the trunk keeps its id (promotion
// only relabels ancestor markers), so no agent is killed.
func (h *handlers) promote(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.PromoteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if h.angelus.Backend == nil {
		return nil, errors.New("promote: no backend (ephemeral angelus)")
	}
	node, ok := h.angelus.Backend.Node(req.FigaroID)
	if !ok {
		return nil, fmt.Errorf("promote: no aria %q", req.FigaroID)
	}
	if node.Kind != conversationKind {
		return nil, fmt.Errorf("promote: %q is a %s node, not a conversation", req.FigaroID, node.Kind)
	}
	climbed, err := h.angelus.Backend.Promote(req.FigaroID, req.Levels)
	if errors.Is(err, store.ErrAtStump) {
		return rpc.PromoteResponse{FigaroID: req.FigaroID, Climbed: 0, AtStump: true}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("promote %q: %w", req.FigaroID, err)
	}
	slog.Info("promoted figaro", "trunk", req.FigaroID, "levels", req.Levels, "climbed", climbed)
	return rpc.PromoteResponse{FigaroID: req.FigaroID, Climbed: climbed}, nil
}

// runtimeFillins returns the per-process boot keys the loadout can't
// supply: the working dir (system.cwd/root), allowlisted env vars, and
// the aria id (non-system, so the agent can read it from a reminder and
// `figaro set --id <id> mantra …`).
func runtimeFillins(ariaID, cwd string) chalkboard.Patch {
	p := chalkboard.Patch{Set: map[string]json.RawMessage{}}
	if b, err := json.Marshal(ariaID); err == nil && ariaID != "" {
		p.Set["aria_id"] = b
	}
	if b, err := json.Marshal(cwd); err == nil {
		p.Set["system.cwd"] = b
		p.Set["system.root"] = b
	}
	if env := chalkboard.EnvironmentPatch(); !env.IsEmpty() {
		for k, v := range env.Set {
			p.Set[k] = v
		}
	}
	return p
}

// convBootPatch is the conversation's boot transition: runtime fill-ins
// plus the per-create req.Patch overrides. The loadout itself is NOT
// re-stated here — it is inherited via the fork watermark and rendered
// in the shared loadout-node prefix.
func convBootPatch(reqPatch *rpc.ChalkboardPatch, ariaID, cwd string) chalkboard.Patch {
	p := runtimeFillins(ariaID, cwd)
	if reqPatch != nil {
		for k, v := range reqPatch.Set {
			p.Set[k] = v
		}
		p.Remove = append(p.Remove, reqPatch.Remove...)
	}
	return p
}

// bootPatchEphemeral is the ephemeral boot: the full resolved loadout
// (no channel to inherit from) plus runtime fill-ins. max_tokens
// defaults when the loadout omits it.
func bootPatchEphemeral(base chalkboard.Patch, ariaID, cwd string) chalkboard.Patch {
	p := chalkboard.Patch{Set: map[string]json.RawMessage{}}
	for k, v := range base.Set {
		p.Set[k] = v
	}
	p.Remove = append(p.Remove, base.Remove...)
	for k, v := range runtimeFillins(ariaID, cwd).Set {
		p.Set[k] = v
	}
	if _, ok := p.Set["system.max_tokens"]; !ok {
		p.Set["system.max_tokens"] = json.RawMessage(`8192`)
	}
	return p
}

// withAriaID returns p with aria_id set (used once the ephemeral id is
// minted).
func withAriaID(p chalkboard.Patch, ariaID string) chalkboard.Patch {
	if b, err := json.Marshal(ariaID); err == nil {
		if p.Set == nil {
			p.Set = map[string]json.RawMessage{}
		}
		p.Set["aria_id"] = b
	}
	return p
}

// patchString reads a string value from a chalkboard.Patch's Set map.
func patchString(p chalkboard.Patch, key string) string {
	raw, ok := p.Set[key]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// patchInt reads an int value from a chalkboard.Patch's Set map.
func patchInt(p chalkboard.Patch, key string) int {
	raw, ok := p.Set[key]
	if !ok {
		return 0
	}
	var n int
	_ = json.Unmarshal(raw, &n)
	return n
}

// patchBool reads a bool value from a chalkboard.Patch's Set map.
func patchBool(p chalkboard.Patch, key string) bool {
	raw, ok := p.Set[key]
	if !ok {
		return false
	}
	var b bool
	_ = json.Unmarshal(raw, &b)
	return b
}

// knobsFromPatch extracts the operational provider knobs from a
// loadout patch's system.* keys.
func knobsFromPatch(p chalkboard.Patch) providerPkg.Knobs {
	return providerPkg.Knobs{
		Model:            patchString(p, "system.model"),
		MaxTokens:        patchInt(p, "system.max_tokens"),
		ReminderRenderer: patchString(p, "system.reminder_renderer"),
		UseOfficialSDK:   patchBool(p, "system.use_official_sdk"),
	}
}

func (h *handlers) kill(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.KillRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}

	// Kill live agent or just remove dormant from disk.
	if h.angelus.Registry.Get(req.FigaroID) != nil {
		if err := h.angelus.Registry.Kill(req.FigaroID); err != nil {
			return nil, err
		}
	}

	if h.angelus.Backend != nil {
		if err := h.angelus.Backend.Remove(req.FigaroID, req.Recursive); err != nil {
			return nil, err // surface "has live branches" etc. to the caller
		}
	}

	slog.Info("killed figaro", "id", req.FigaroID)
	return rpc.KillResponse{OK: true}, nil
}

// list merges live and dormant arias.
func (h *handlers) list(ctx context.Context, params json.RawMessage) (interface{}, error) {
	// IDsOnly skips the per-aria chalkboard + node fills (the slow part) — used
	// by completion, which only needs the ids. Tolerant of nil/empty params.
	var req rpc.ListRequest
	_ = json.Unmarshal(params, &req)

	live := h.angelus.Registry.List()
	result := make([]rpc.FigaroInfoResponse, 0, len(live))
	seen := make(map[string]struct{}, len(live))
	for _, info := range live {
		seen[info.ID] = struct{}{}
		entry := rpc.FigaroInfoResponse{
			ID:               info.ID,
			State:            info.State,
			Provider:         info.Provider,
			Model:            info.Model,
			MessageCount:     info.MessageCount,
			TokensIn:         info.TokensIn,
			TokensOut:        info.TokensOut,
			CacheReadTokens:  info.CacheReadTokens,
			CacheWriteTokens: info.CacheWriteTokens,
			ContextTokens:    info.ContextTokens,
			ContextExact:     info.ContextExact,
			CreatedAt:        info.CreatedAt.UnixMilli(),
			LastActive:       info.LastActive.UnixMilli(),
			BoundPIDs:        h.angelus.Registry.BoundPIDs(info.ID),
		}
		if !req.IDsOnly {
			h.fillFromChalkboard(info.ID, &entry) // mantra + cwd from the saved chalkboard
		}
		result = append(result, entry)
	}

	// Snapshot the whole forest ONCE per request. Each Backend.Nodes() is a
	// full disk scan (figwal opens every trunk head for Tip/lineage), so the
	// dormant-aria fill, the global anchors, AND the per-entry forest position
	// all share this single snapshot + its id index. (Previously this path
	// called Backend.List() + Backend.Nodes() + a per-entry Backend.Node() —
	// O(N) full scans => O(N^2) trunk-head opens on a big tree.)
	var nodeList []store.NodeView
	nodeByID := map[string]store.NodeView{}
	if h.angelus.Backend != nil {
		nodeList = h.angelus.Backend.Nodes()
		for _, n := range nodeList {
			nodeByID[n.ID] = n
		}
	}

	// Dormant conversation trunks (not currently registered/live).
	for _, n := range nodeList {
		if n.Kind != conversationKind {
			continue
		}
		if _, ok := seen[n.ID]; ok {
			continue
		}
		seen[n.ID] = struct{}{}
		entry := rpc.FigaroInfoResponse{ID: n.ID, State: "dormant"}
		if m, _ := h.angelus.Backend.Meta(n.ID); m != nil {
			entry.MessageCount = m.MessageCount // sidecar fast-path (tokens + IDsOnly)
			entry.TokensIn = m.TokensIn
			entry.TokensOut = m.TokensOut
			entry.CacheReadTokens = m.CacheReadTokens
			entry.CacheWriteTokens = m.CacheWriteTokens
			if m.LastActiveMS != 0 {
				entry.LastActive = m.LastActiveMS
			}
		}
		if !req.IDsOnly {
			// MSGS is the canonical count from the live head (single source of
			// truth) — not the sidecar, which a pre-heal binary may have written
			// with a different convention. Self-heals the sidecar. Opens the head
			// (only when actually displaying, not for id-only completion).
			if c, ok := h.angelus.Backend.CanonicalCount(n.ID); ok {
				entry.MessageCount = c
			}
			h.fillFromChalkboard(n.ID, &entry)
		}
		result = append(result, entry)
	}

	// Global: also surface the ceremonial anchors — the null genesis trunk and
	// every versioned loadout — that the conversation filter above skips.
	// fillFromNode below stamps their Kind/Loadout/Version/Parent.
	if req.Global {
		for _, n := range nodeList {
			if n.Kind == conversationKind {
				continue
			}
			if _, ok := seen[n.ID]; ok {
				continue
			}
			seen[n.ID] = struct{}{}
			result = append(result, rpc.FigaroInfoResponse{ID: n.ID, State: "anchor"})
		}
	}

	// Forest position for every entry (live + dormant), from the snapshot.
	if !req.IDsOnly {
		for i := range result {
			h.fillFromNode(nodeByID, &result[i])
		}
	}

	return rpc.ListResponse{Figaros: result}, nil
}

// fillFromNode adds the fork-forest position (vector/trunk/parent/frozen)
// from the tree, marking frozen nodes' state. The forest is snapshotted by
// the caller (once per request) and indexed by id, so this is a map lookup.
func (h *handlers) fillFromNode(nodes map[string]store.NodeView, entry *rpc.FigaroInfoResponse) {
	n, ok := nodes[entry.ID]
	if !ok {
		return
	}
	entry.Vector = n.Vector
	entry.Trunk = n.Trunk
	entry.Parent = n.Parent
	entry.Frozen = n.Frozen
	entry.BranchedLT = n.BranchedLT
	entry.Kind = n.Kind
	// Ceremonial loadout anchors carry their name + a live/stale label here
	// (conversations get those from their chalkboard stamp instead).
	if n.Kind == string(loadoutKind) {
		entry.LoadoutName = n.Loadout
		entry.LoadoutVer = loadoutVerLabel(n.Version, h.currentLoadoutHash(n.Loadout))
	}
	if n.Frozen && entry.State != "active" {
		entry.State = "frozen"
	}
}

// loadoutKind / nullKind / conversationKind mirror the store's nodeKind string
// values (the store package's constants are unexported).
const (
	nullKind         = "null"
	loadoutKind      = "loadout"
	conversationKind = "conversation"
)

func (h *handlers) bind(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.BindRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	// Lazy-restore dormant arias before bind.
	if _, err := h.restoreByID(ctx, req.FigaroID); err != nil {
		return nil, fmt.Errorf("bind: restore %s: %w", req.FigaroID, err)
	}
	if err := h.angelus.Registry.Bind(req.PID, req.FigaroID, req.AtMainLT); err != nil {
		return nil, err
	}
	return rpc.BindResponse{OK: true}, nil
}

// attach restores a dormant aria without touching pid bindings.
func (h *handlers) attach(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.AttachRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := rpc.ValidateAriaID(req.FigaroID); err != nil {
		return nil, err
	}
	f, err := h.restoreByID(ctx, req.FigaroID)
	if err != nil {
		return nil, fmt.Errorf("attach %s: %w", req.FigaroID, err)
	}
	return rpc.AttachResponse{
		FigaroID: req.FigaroID,
		Endpoint: rpc.Endpoint{
			Scheme:  "unix",
			Address: f.SocketPath(),
		},
	}, nil
}

func (h *handlers) resolve(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ResolveRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	id, f, lt := h.angelus.Registry.Resolve(req.PID)
	if f == nil {
		return rpc.ResolveResponse{Found: false}, nil
	}
	return rpc.ResolveResponse{
		FigaroID: id,
		Endpoint: rpc.Endpoint{
			Scheme:  "unix",
			Address: f.SocketPath(),
		},
		Found:    true,
		AtMainLT: lt,
	}, nil
}

func (h *handlers) unbind(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.UnbindRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	h.angelus.Registry.Unbind(req.PID)
	return rpc.UnbindResponse{OK: true}, nil
}

func (h *handlers) status(ctx context.Context, params json.RawMessage) (interface{}, error) {
	return rpc.StatusResponse{
		Uptime:      h.angelus.StartedAt.UnixMilli(),
		FigaroCount: h.angelus.Registry.FigaroCount(),
		BoundPIDs:   h.angelus.Registry.BoundPIDCount(),
	}, nil
}

func (h *handlers) saveBindings(ctx context.Context, params json.RawMessage) (interface{}, error) {
	path := h.angelus.BindingsPath()
	if err := SaveBindings(h.angelus.Registry, path); err != nil {
		return nil, err
	}
	slog.Info("saved pid bindings", "path", path, "count", h.angelus.Registry.BoundPIDCount())
	return rpc.SaveBindingsResponse{
		OK:    true,
		Count: h.angelus.Registry.BoundPIDCount(),
	}, nil
}

// ariaReadHardCap bounds Limit on aria.read regardless of what the
// client asks for, so a misconfigured client can't pull megabytes of
// IR in a single RPC.
const ariaReadHardCap = 1000

// ariaRead serves IR entries for an aria through the shared LogCache.
// Live agents share the same Log instance, so reads run lock-free
// against the agent's writes. For dormant arias the cache opens on
// miss and the entry TTLs out naturally.
func (h *handlers) ariaRead(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.AriaReadRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, fmt.Errorf("aria.read: parse params: %w", err)
		}
	}
	if req.FigaroID == "" {
		return nil, errors.New("aria.read: empty figaro_id")
	}
	if h.angelus.Backend == nil {
		return nil, errors.New("aria.read: no backend (ephemeral angelus)")
	}

	// The node must exist in the tree; otherwise Open would materialize
	// an empty branch for a typo'd id.
	if _, ok := h.angelus.Backend.Node(req.FigaroID); !ok {
		return nil, fmt.Errorf("aria.read: no aria %q in tree", req.FigaroID)
	}

	// The backend returns the same shared, memoized IR instance the live
	// agent holds, so reads run lock-free against its writes.
	log, err := h.angelus.Backend.Open(req.FigaroID)
	if err != nil {
		return nil, fmt.Errorf("aria.read: open: %w", err)
	}

	all := log.Read()
	total := len(all)

	limit := req.Limit
	if limit <= 0 || limit > ariaReadHardCap {
		limit = ariaReadHardCap
	}

	// Keyset pagination: Before takes precedence over From.
	if req.Before > 0 {
		var selected []store.Entry[message.Message]
		for i := len(all) - 1; i >= 0 && len(selected) < limit; i-- {
			if all[i].LT < req.Before {
				selected = append(selected, all[i])
			}
		}
		// Reverse to chronological order.
		for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
			selected[i], selected[j] = selected[j], selected[i]
		}
		entries := make([]rpc.AriaReadEntry, len(selected))
		for i, e := range selected {
			raw, _ := json.Marshal(e.Payload)
			entries[i] = rpc.AriaReadEntry{LT: e.LT, Payload: raw}
		}
		var nextBefore uint64
		if len(selected) > 0 {
			nextBefore = selected[0].LT
		}
		return &rpc.AriaReadResponse{Entries: entries, Total: total, NextFrom: nextBefore}, nil
	}

	from := req.From
	startIdx := 0
	if from > 0 {
		for i, e := range all {
			if e.LT >= from {
				startIdx = i
				break
			}
			if i == len(all)-1 {
				startIdx = len(all)
			}
		}
	}

	endIdx := startIdx + limit
	if endIdx > len(all) {
		endIdx = len(all)
	}

	out := make([]rpc.AriaReadEntry, 0, endIdx-startIdx)
	for _, e := range all[startIdx:endIdx] {
		raw, mErr := json.Marshal(e.Payload)
		if mErr != nil {
			return nil, fmt.Errorf("aria.read: marshal LT=%d: %w", e.LT, mErr)
		}
		out = append(out, rpc.AriaReadEntry{LT: e.LT, Payload: raw})
	}
	var nextFrom uint64
	if endIdx < len(all) {
		nextFrom = all[endIdx].LT
	}
	return rpc.AriaReadResponse{
		Entries:  out,
		Total:    total,
		NextFrom: nextFrom,
	}, nil
}

// restoreByID re-creates a figaro from the backend tree.
func (h *handlers) restoreByID(ctx context.Context, ariaID string) (figaro.Figaro, error) {
	if f := h.angelus.Registry.Get(ariaID); f != nil {
		return f, nil
	}
	if h.angelus.Backend == nil {
		return nil, fmt.Errorf("no backend configured")
	}
	node, ok := h.angelus.Backend.Node(ariaID)
	if !ok {
		return nil, fmt.Errorf("aria %q not found in tree", ariaID)
	}
	if node.Kind != "conversation" {
		return nil, fmt.Errorf("aria %q is a %s node, not a conversation", ariaID, node.Kind)
	}
	if node.Frozen {
		return nil, fmt.Errorf("aria %q is frozen (a fork point); attach a child", ariaID)
	}
	return h.restoreOne(ctx, ariaID)
}

// restoreOne builds and registers a figaro for an existing conversation
// node, seeding its chalkboard from the channel.
func (h *handlers) restoreOne(ctx context.Context, ariaID string) (figaro.Figaro, error) {
	cb := h.openAriaChalkboard(ariaID)
	if cb == nil {
		return nil, fmt.Errorf("restore %s: chalkboard unavailable", ariaID)
	}
	cbSnap := cb.Snapshot()
	cbStr := func(key string) string {
		raw, ok := cbSnap[key]
		if !ok {
			return ""
		}
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	cbInt := func(key string) int {
		raw, ok := cbSnap[key]
		if !ok {
			return 0
		}
		var n int
		_ = json.Unmarshal(raw, &n)
		return n
	}
	cbBool := func(key string) bool {
		raw, ok := cbSnap[key]
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

	agent := figaro.NewAgent(figaro.Config{
		ID:         ariaID,
		SocketPath: sockPath,
		Provider:   prov,
		Outfitter:  h.outfitter,
		Tools:      tool.DefaultRegistryFn(cwdFromChalkboard(cb, toolRoot)),
		Backend:    h.angelus.Backend,
		Chalkboard: cb,
	})

	if err := h.angelus.Registry.Register(agent); err != nil {
		agent.Kill()
		return nil, fmt.Errorf("restore %s: register: %w", ariaID, err)
	}

	go agent.StartSocket(ctx)

	slog.Info("restored figaro",
		"id", ariaID, "provider", provName, "model", knobs.Model)
	return agent, nil
}

// cwdFromChalkboard returns a closure that reads system.cwd from
// cbState at call time, falling back to fallback when the key is
// unset, the chalkboard is nil, or the value isn't a JSON string.
//
// This is the seam that lets the bash tool honor a runtime
// `figaro set system.cwd …` without rebuilding the registry.
func cwdFromChalkboard(cbState *chalkboard.State, fallback string) func() string {
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

// errNoDefaultLoadout builds a typed JSON-RPC error directing the
// client to drive first-run loadout selection.
func (h *handlers) errNoDefaultLoadout() error {
	data, _ := json.Marshal(rpc.ErrorData{AvailableProviders: h.availableProviders})
	return &jkrpc.Error{
		Code:    rpc.ErrNoDefaultLoadout,
		Message: "no default loadout configured",
		Data:    data,
	}
}

// errNoProvider builds a typed JSON-RPC error indicating the
// resolved loadout has no system.provider key.
func (h *handlers) errNoProvider(loadoutName string) error {
	data, _ := json.Marshal(rpc.ErrorData{
		AvailableProviders: h.availableProviders,
		Loadout:            loadoutName,
	})
	return &jkrpc.Error{
		Code:    rpc.ErrNoProvider,
		Message: fmt.Sprintf("loadout %q has no system.provider", loadoutName),
		Data:    data,
	}
}

// errLoadoutNotFound builds a typed JSON-RPC error for a missing
// named loadout. cause carries the underlying outfit error.
func (h *handlers) errLoadoutNotFound(name string, cause error) error {
	data, _ := json.Marshal(rpc.ErrorData{
		Name:        name,
		SearchPaths: []string{h.config.LoadoutPath(name)},
	})
	return &jkrpc.Error{
		Code:    rpc.ErrLoadoutNotFound,
		Message: fmt.Sprintf("loadout %q not found: %s", name, cause),
		Data:    data,
	}
}
