package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"text/template"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/authz"
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
	"github.com/jack-work/figaro/internal/turns"
	"github.com/jack-work/figaro/internal/uiir"
	"github.com/jack-work/figwal/segment"
	"github.com/jack-work/jkrpc"
)

// ProviderFactory creates providers for an Agent; instances never span
// arias. The agent keeps the factory so it can rebind mid-conversation when
// system.provider changes on the chalkboard (see figaro.syncProvider).
type ProviderFactory = figaro.ProviderFactory

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
		Map: authz.Guard(map[string]jkrpc.HandlerFunc{
			rpc.MethodCreate:         h.create,
			rpc.MethodFork:           h.fork,
			rpc.MethodPromote:        h.promote,
			rpc.MethodImport:         h.importAria,
			rpc.MethodNormalize:      h.normalize,
			rpc.MethodGC:             h.gc,
			rpc.MethodKill:           h.kill,
			rpc.MethodList:           h.list,
			rpc.MethodAttach:         h.attach,
			rpc.MethodBind:           h.bind,
			rpc.MethodResolve:        h.resolve,
			rpc.MethodUnbind:         h.unbind,
			rpc.MethodStatus:         h.status,
			rpc.MethodSaveBindings:   h.saveBindings,
			rpc.MethodAriaRead:       h.ariaRead,
			rpc.MethodAriaPage:       h.ariaPage,
			rpc.MethodAriaContext:    h.ariaContext,
			rpc.MethodAriaChalkboard: h.ariaChalkboard,
		}, h.authenticator(), h.policy()),
		h: h,
	}
}

// authenticator builds the configured authn provider. Disabled by default, so
// a config that says nothing behaves as figaro did before this existed.
func (h *handlers) authenticator() authz.Authenticator {
	return authz.AriaHeader{Enabled: h.config != nil && h.config.CallerIdentityEnabled()}
}

// policy builds the configured authorization policy. The turn-active predicate
// is read off the live registry: a dormant or unknown aria has no turn in
// flight, so it cannot be mid-turn, and a fork of it is free to proceed.
//
// TurnActive is used rather than Info().State because Info takes the agent's
// lock and can block (TestRegistryListDoesNotHoldRegistryLockDuringInfo); a
// policy check that stalls on the very agent it is guarding would reintroduce
// the hazard this rule exists to catch.
func (h *handlers) policy() authz.Policy {
	name := "allow-all"
	if h.config != nil {
		name = h.config.AuthzPolicy()
	}
	switch name {
	case "default":
		return authz.DefaultRules(h.turnActive)
	default:
		return authz.AllowAll()
	}
}

func (h *handlers) turnActive(ariaID string) bool {
	if h.angelus == nil || h.angelus.Registry == nil {
		return false
	}
	live := h.angelus.Registry.Get(ariaID)
	return live != nil && live.TurnActive()
}

// Restore lazily re-creates the agent for ariaID.
func (hs *Handlers) Restore(ctx context.Context, ariaID string) (figaro.Figaro, error) {
	return hs.h.restoreByID(ctx, ariaID)
}

// OpenEndpoint makes an aria dialable without waking it. It is what binding
// needs: the socket has to be listening before anyone is handed the path,
// and nothing more than that has to exist.
func (hs *Handlers) OpenEndpoint(ariaID string) error {
	if err := hs.h.requireAria(ariaID); err != nil {
		return err
	}
	_, err := hs.h.hubFor(ariaID)
	return err
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

	restoreMu    sync.Mutex
	restoreLocks map[string]*sync.Mutex
}

type listEnrichment struct {
	index  int
	ariaID string
}

// reloadConfigIfChanged re-reads config.toml from disk when the
// in-memory copy looks stale relative to a wizard write. We're
// conservative: only reload when the in-memory DefaultOutfit is
// empty AND a config.toml exists on disk. This means tests that
// inject loaded.Config.DefaultOutfit in memory without a backing
// file are untouched, while the production case (first-run wizard
// writes config.toml + an outfit, then retries Create) sees the
// fresh value.
func (h *handlers) reloadConfigIfChanged() {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	if h.config.Config.DefaultOutfit != "" {
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
	if snap.Len() > 0 {
		st.Apply(snap.AsPatch())
	}
	return st
}

// currentOutfitHash is the content hash the outfit would have right now
// (recomputed from the on-disk definition), or "" if it can't be loaded.
//
// This used to memoize with a 3-second TTL, because folding an outfit re-read
// every skill file and `list` calls this once per aria. The Outfitter now
// caches its folds against the files they were built from, so the repeat cost
// is a stat per dependency — cheaper than the TTL was, and never stale.
func (h *handlers) currentOutfitHash(name string) string {
	if h.outfitter == nil {
		return ""
	}
	p, err := h.outfitter.Load(name)
	if err != nil {
		return ""
	}
	body, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	hash, _ := segment.ValueHash(body)
	return hash
}

// outfitVerLabel renders the version column: "live" when the stamped hash
// matches the current one, else the stamped hash's first 8 chars.
func outfitVerLabel(stamped, current string) string {
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

func (h *handlers) create(ctx context.Context, params json.RawMessage) (interface{}, error) {
	_, span := figOtel.Start(ctx, "angelus.create")
	defer span.End()

	var req rpc.CreateRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}

	// Resolve the outfit name. Empty request → configured default →
	// typed JSON-RPC error so the client can drive first-run setup.
	//
	// We re-read config.toml from disk first so that wizard-driven
	// changes (the first-run flow scaffolds an outfit + sets
	// default_outfit, then retries this Create call) are picked up
	// without a daemon restart. One os.ReadFile + toml.Unmarshal per
	// request is cheap relative to anything downstream.
	h.reloadConfigIfChanged()
	outfitName := req.Outfit
	if outfitName == "" {
		outfitName = h.config.Config.DefaultOutfit
	}
	if outfitName == "" {
		return nil, h.errNoDefaultOutfit()
	}

	// Resolve outfit -> chalkboard patch. Missing files are not
	// fatal; the patch comes back empty and req.Patch may still
	// supply system.provider. outfitPatch is the STABLE outfit (it
	// defines the outfit node's identity/version); base layers the
	// per-create req.Patch overrides on top for provider/knob resolution.
	outfitPatch, err := h.outfitter.Load(outfitName)
	if err != nil {
		return nil, h.errOutfitNotFound(outfitName, err)
	}
	base := chalkboard.Patch{Set: map[string]json.RawMessage{}}
	for k, v := range outfitPatch.Set {
		base.Set[k] = v
	}
	base.Remove = append(base.Remove, outfitPatch.Remove...)
	if req.Patch != nil {
		for k, v := range req.Patch.Set {
			base.Set[k] = v
		}
		base.Remove = append(base.Remove, req.Patch.Remove...)
	}

	provName := patchString(base, "system.provider")
	if provName == "" {
		return nil, h.errNoProvider(outfitName)
	}
	knobs := knobsFromPatch(base)

	span.SetAttributes(
		attribute.String("figaro.outfit", outfitName),
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
		// Ephemeral: no channel. Seed state with the full outfit +
		// runtime fill-ins, and fold the same patch on the first message so
		// reminders render.
		id = uuid.New().String()[:8]
		boot := bootPatchEphemeral(base, "", cwd) // id filled below
		boot = withAriaID(boot, id)
		cbState.Apply(boot)
		bp := boot
		inlineBoot = &bp
	} else {
		// Materialize/reuse the outfit node (identity = stable outfit
		// patch), fork it into a fresh conversation, then write the
		// per-conversation boot transition (runtime fill-ins + req.Patch
		// overrides) to its chalkboard channel. The outfit's own
		// reminders render in the shared outfit-node prefix.
		outfitID, lerr := backend.CreateOutfit(outfitName, outfitPatch)
		if lerr != nil {
			return nil, fmt.Errorf("create outfit node: %w", lerr)
		}
		var cerr error
		id, cerr = backend.CreateConversation(outfitID)
		if cerr != nil {
			return nil, fmt.Errorf("create conversation: %w", cerr)
		}
		boot := convBootPatch(req.Patch, id, cwd)
		if !boot.IsEmpty() {
			if _, aerr := backend.ApplyChalkboard(id, boot); aerr != nil {
				return nil, fmt.Errorf("seed conversation chalkboard: %w", aerr)
			}
		}
		snap, serr := backend.ChalkboardState(id)
		if serr != nil {
			return nil, fmt.Errorf("read conversation chalkboard: %w", serr)
		}
		cbState.Apply(snap.AsPatch())
	}

	sockPath := filepath.Join(h.angelus.FigaroSocketDir(), id+".sock")

	reg := tool.DefaultRegistryForAria(id, cwdFromChalkboard(cbState, cwd),
		tool.WithImageBudget(h.config.InlineImageBudget()),
		tool.WithSessions(h.angelus.Sessions))
	agent := figaro.NewAgent(figaro.Config{
		ID:              id,
		SocketPath:      sockPath,
		Provider:        prov,
		ProviderFactory: h.factory,
		Outfitter:       h.outfitter,
		Tools:           reg,
		Projector:       uiir.New(reg),
		Backend:         backend,
		Chalkboard:      cbState,
		InlineBoot:      inlineBoot,
		Settings:        h.config,
	})

	if err := h.angelus.Registry.Register(agent); err != nil {
		agent.Kill()
		return nil, err
	}

	// The endpoint is the angelus's, not the agent's: it must already be
	// listening when this response hands the caller a path to dial, and it
	// must still be listening after the agent is reclaimed.
	unbind, herr := h.bindAgentToHub(id, agent)
	if herr != nil {
		agent.Kill()
		return nil, fmt.Errorf("create %s: open endpoint: %w", id, herr)
	}
	agent.OnTeardown(unbind)

	slog.Info("created figaro",
		"id", id, "outfit", outfitName, "provider", provName, "model", knobs.Model, "socket", sockPath)

	return rpc.CreateResponse{
		FigaroID: id,
		Endpoint: rpc.Endpoint{
			Scheme:  "unix",
			Address: sockPath,
		},
	}, nil
}

// fork branches a conversation at its head. The addressed trunk keeps its id
// and remains live; the alternative is a new dormant conversation.
// forkPointOf maps a turn id to the main-LT a fork takes.
//
// Turn N's fork point is the LT that ENDS turn N-1: the branch then retains
// everything through the previous exchange and the caller's new prompt becomes
// turn N. That boundary is the tail of a completed exchange, so a tool_invoke
// is never left without its result.
//
// figwal retains [First, atMainLT] INCLUSIVE and the branch begins at
// atMainLT+1, which is why this is first-1 and not first.
// checkForkLT refuses an LT past the aria's own tail. A turn is validated
// by lookup ("it has turns 1..N"); an LT is a raw coordinate, so without
// this a typo forks at a point that does not exist yet and the branch
// silently inherits everything instead of the prefix the user asked for.
func (h *handlers) checkForkLT(ariaID string, lt uint64) error {
	if lt == 0 {
		return nil
	}
	log, err := h.angelus.Backend.Open(ariaID)
	if err != nil {
		return fmt.Errorf("fork: open %s: %w", ariaID, err)
	}
	entries := log.Read()
	var tail uint64
	if n := len(entries); n > 0 {
		tail = entries[n-1].LT
	}
	if lt > tail {
		return fmt.Errorf("aria %s has no LT %d (its logical time runs 1..%d)", ariaID, lt, tail)
	}
	return nil
}

func (h *handlers) forkPointOf(ariaID string, turn uint64) (uint64, error) {
	if turn == 0 {
		return 0, nil // head fork
	}
	log, err := h.angelus.Backend.Open(ariaID)
	if err != nil {
		return 0, fmt.Errorf("fork: open %s: %w", ariaID, err)
	}
	entries := log.Read()
	msgs := make([]message.Message, len(entries))
	for i, e := range entries {
		msgs[i] = e.Payload
		msgs[i].LogicalTime = e.LT
	}
	first, _, ok := turns.Span(msgs, turn)
	if !ok {
		if last := turns.StampIDs(msgs); last > 0 {
			return 0, fmt.Errorf("aria %s has no turn %d (it has turns 1..%d)", ariaID, turn, last)
		}
		return 0, fmt.Errorf("aria %s has no turns yet", ariaID)
	}
	return first - 1, nil
}

func (h *handlers) fork(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ForkRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if h.angelus.Backend == nil {
		return nil, errors.New("fork: no backend (ephemeral angelus)")
	}
	var cont, alt string
	note := ""
	var forkOwner store.OwnerInfo
	// Two coordinates, and the server owns the translation between them.
	// A TURN is what a human names and what `fig show` prints; an LT is the
	// model's own step count. Both name a fork point; neither can be
	// inferred from the other's magnitude, which is exactly how an LT once
	// arrived in the turn field and made every `send <id>:<turn>` fail.
	if req.AtTurn > 0 && req.AtLT > 0 {
		return nil, fmt.Errorf("fork: give a turn (:%d) or an LT (.%d), not both", req.AtTurn, req.AtLT)
	}
	atMainLT := req.AtLT
	if req.AtTurn > 0 {
		lt, terr := h.forkPointOf(req.FigaroID, req.AtTurn)
		if terr != nil {
			return nil, terr
		}
		atMainLT = lt
	}
	if err := h.checkForkLT(req.FigaroID, req.AtLT); err != nil {
		return nil, err
	}
	// The COORDINATE says whether this is interior; atMainLT says where.
	// They are not the same question: forking at turn 1 retains nothing
	// before it, so its LT is 0 -- which is also the head-fork sentinel.
	// Reading interior-ness off the LT collapsed "replace the first turn"
	// into "fork at the head".
	interior := req.AtTurn > 0 || req.AtLT > 0
	where := fmt.Sprintf("turn %d", req.AtTurn)
	if req.AtLT > 0 {
		where = fmt.Sprintf("LT %d", req.AtLT)
	}
	if interior {
		if owner, err := h.angelus.Backend.OwnerResolution(req.FigaroID, atMainLT); err == nil {
			forkOwner = owner
			switch {
			case owner.IsRoot:
				note = fmt.Sprintf("%s is the genesis root — spawned a fresh outfitless conversation there", where)
			case owner.Outfit != "":
				note = fmt.Sprintf("%s is in outfit %s — spawned a fresh conversation under it", where, owner.Outfit)
			case owner.Trunk != "" && owner.Trunk != req.FigaroID:
				note = fmt.Sprintf("%s lives in trunk %s — branching there", where, owner.Trunk)
			}
		}
	}
	runFork := func() error {
		parentMeta := h.forkMetaSnapshot(req.FigaroID)
		var err error
		if interior {
			cont, alt, err = h.angelus.Backend.ForkAt(req.FigaroID, atMainLT)
		} else {
			cont, alt, err = h.angelus.Backend.Fork(req.FigaroID)
		}
		if err != nil {
			return err
		}
		h.seedForkMeta(parentMeta, req.FigaroID, alt, atMainLT, interior, forkOwner)
		// The alternative inherits the parent's chalkboard — including the
		// parent's aria_id. Re-stamp so the forked agent knows its own id
		// (a normal state transition it sees on its next turn); without
		// this an aria cannot reliably fork itself.
		if alt != "" && alt != req.FigaroID {
			if b, merr := json.Marshal(alt); merr == nil {
				if _, perr := h.angelus.Backend.ApplyChalkboard(alt, message.Patch{
					Set: map[string]json.RawMessage{"aria_id": b},
				}); perr != nil {
					slog.Warn("fork: restamp aria_id", "alt", alt, "err", perr)
				}
			}
		}
		return nil
	}

	// Run the fork here, not on the target's actor.
	//
	// It used to be handed to the agent's inbox and waited on, so that a fork
	// could not re-home the log while the agent was appending to it. figwal
	// already guarantees that: Trunks.Append and the flat creators both take
	// lockLineage(trunk), so they are mutually excluded whoever calls them.
	// The inbox hop was a second lock over the first.
	//
	// And it was the second lock that deadlocked. A figaro forking ITSELF does
	// so from a tool call, which runs on its own drain loop; the fork then
	// queued behind a turn that could not finish until the tool call returned,
	// and the tool call could not return until the fork ran. An aria could not
	// fork itself, which is the one caller that most wants to.
	//
	// This is the cure the deferred note here asked for, arrived at from the
	// other end: rather than move trunk state off the actor, stop routing the
	// fork through the actor at all. authz.NoSelfForkDuringTurn stays as a
	// guardrail, but it no longer guards a hang.
	err := runFork()
	if err != nil {
		return nil, fmt.Errorf("fork %q: %w", req.FigaroID, err)
	}
	slog.Info("forked figaro", "parent", req.FigaroID, "turn", req.AtTurn, "lt", atMainLT, "continuation", cont, "alternative", alt)
	return rpc.ForkResponse{Parent: req.FigaroID, Continuation: cont, Alternative: alt, OwnerNote: note}, nil
}

func (h *handlers) forkMetaSnapshot(parent string) *store.AriaMeta {
	meta, _ := h.angelus.Backend.Meta(parent)
	if meta == nil {
		meta = &store.AriaMeta{}
	}
	copy := *meta
	var live figaro.Figaro
	if h.angelus.Registry != nil {
		live = h.angelus.Registry.Get(parent)
	}
	if live != nil {
		info := live.Info()
		copy.MessageCount = info.MessageCount
		copy.TokensIn = info.TokensIn
		copy.TokensOut = info.TokensOut
		copy.CacheReadTokens = info.CacheReadTokens
		copy.CacheWriteTokens = info.CacheWriteTokens
		copy.Provider = info.Provider
		copy.Model = info.Model
		copy.Mantra = info.Mantra
		copy.Cwd = info.Cwd
		copy.OutfitName = info.OutfitName
		copy.OutfitVersion = info.OutfitVersion
		copy.ContextTokens = info.ContextTokens
		copy.ContextLimit = info.ContextLimit
		copy.ContextExact = info.ContextExact
		copy.CreatedAtMS = info.CreatedAt.UnixMilli()
		copy.LastActiveMS = info.LastActive.UnixMilli()
		copy.LastFigaroLT = info.LastFigaroLT
	}
	return &copy
}

// interior says whether this was a turn-addressed fork. atMainLT alone cannot:
// forking at turn 1 has LT 0, which is also the head-fork sentinel.
func (h *handlers) seedForkMeta(meta *store.AriaMeta, parent, child string, atMainLT uint64, interior bool, owner store.OwnerInfo) {
	if meta == nil {
		return
	}
	copy := *meta
	now := time.Now().UnixMilli()
	copy.CreatedAtMS = now
	copy.LastActiveMS = now
	if interior {
		copy.MessageCount = h.messageCountAt(parent, atMainLT)
		copy.TurnCount = 0
		copy.TokensIn = 0
		copy.TokensOut = 0
		copy.CacheReadTokens = 0
		copy.CacheWriteTokens = 0
		copy.ContextTokens = 0
		copy.ContextLimit = 0
		copy.ContextExact = false
		copy.LastFigaroLT = atMainLT
		copy.Provider = ""
		copy.Model = ""
		copy.Mantra = ""
		copy.Cwd = ""
		copy.OutfitName = ""
		copy.OutfitVersion = ""
		if !owner.IsRoot {
			outfitID := owner.Outfit
			if outfitID == "" {
				outfitID = h.outfitAncestor(parent)
			}
			if outfit, ok := h.angelus.Backend.Node(outfitID); ok && outfit.Kind == string(outfitKind) {
				copy.OutfitName = outfit.Outfit
				copy.OutfitVersion = outfit.Version
			}
		}
	}

	if err := h.angelus.Backend.SetMeta(child, &copy); err != nil {
		slog.Warn("seed fork metadata", "aria", child, "err", err)
	}
}

func (h *handlers) outfitAncestor(id string) string {
	for id != "" {
		node, ok := h.angelus.Backend.Node(id)
		if !ok {
			return ""
		}
		if node.Kind == string(outfitKind) {
			return node.ID
		}
		id = node.Parent
	}
	return ""
}

func (h *handlers) messageCountAt(id string, atMainLT uint64) int {
	count := int(atMainLT)
	if count > 0 {
		count-- // root genesis
	}
	for count > 0 {
		node, ok := h.angelus.Backend.Node(id)
		if !ok || node.Parent == "" {
			break
		}
		if parent, ok := h.angelus.Backend.Node(node.Parent); ok && parent.Kind == string(outfitKind) {
			count-- // outfit birth
			break
		}
		id = node.Parent
	}
	return max(0, count)
}

// promote climbs a conversation trunk up N stump-bounded levels (it absorbs
// its parent trunk's run). A live agent on the trunk keeps its id (promotion
// only relabels ancestor markers), so no agent is killed.
// gc collects outfit stumps nothing is using.
//
// A stump is content-addressed (<outfit>@<hash>), so one accumulates per outfit
// VERSION: every edit to an outfit mints a new one the next time an aria is
// born under it, and until stumps became collectible nothing ever took the old
// ones away. Killing an aria now collects its stump when it was the last
// child; this is the sweep for everything that predates that.
//
// Collecting loses nothing — the next aria wanting that outfit re-mints the
// same id — so the only question is whether anything is still under it, which
// the topology answers directly.
func (h *handlers) gc(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.GCRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
	}
	if h.angelus.Backend == nil {
		return nil, errors.New("gc: no backend (ephemeral angelus)")
	}

	nodes := h.angelus.Backend.Nodes()
	children := map[string]int{}
	for _, n := range nodes {
		if n.Parent != "" {
			children[n.Parent]++
		}
	}

	resp := rpc.GCResponse{DryRun: req.DryRun}
	for _, n := range nodes {
		if n.Kind != "outfit" {
			continue
		}
		entry := rpc.GCStump{
			ID: n.ID, Outfit: n.Outfit, Version: n.Version,
			Children: children[n.ID],
		}
		if entry.Children == 0 {
			entry.Collected = true
			if !req.DryRun {
				if err := h.angelus.Backend.CollectStump(n.ID); err != nil {
					entry.Collected, entry.Err = false, err.Error()
				}
			}
			if entry.Collected {
				resp.Collected++
			}
		}
		resp.Stumps = append(resp.Stumps, entry)
	}
	sort.Slice(resp.Stumps, func(i, j int) bool { return resp.Stumps[i].ID < resp.Stumps[j].ID })
	if !req.DryRun && resp.Collected > 0 {
		slog.Info("collected outfit stumps", "count", resp.Collected)
	}
	return resp, nil
}

func (h *handlers) normalize(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.NormalizeRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if h.angelus.Backend == nil {
		return nil, errors.New("normalize: no backend (ephemeral angelus)")
	}
	if req.Segments {
		return nil, errors.New("normalize: --segments is not implemented yet")
	}
	n, err := h.angelus.Backend.Normalize()
	if errors.Is(err, store.ErrNoTrunkCapability) {
		return rpc.NormalizeResponse{Unsupported: true}, nil
	}
	if err != nil {
		return nil, err
	}
	slog.Info("normalized topology", "detached", n)
	return rpc.NormalizeResponse{Detached: n}, nil
}

// importAria restores an exported aria as a NEW conversation.
//
// It grafts nothing. The outfit is resolved by content (CreateOutfit is
// content-addressed, so an identical outfit is reused rather than
// duplicated), a conversation is spawned under it, and the messages are
// appended through the ordinary path. Every identity — node id, fork base, LT
// — is minted by THIS store, which is why an import can never collide with
// what is already here and never needs a renumbering pass.
//
// What it deliberately does not carry: the provider translation caches. They
// are a derivable wire cache, and the price of dropping them is one cache-miss
// on the next turn (which, per the anthropic assembler, replays without
// thinking blocks rather than with unsigned ones). Exactness is the graft's
// job — see proposals/aria-graft.md — not this one's.
func (h *handlers) importAria(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ImportRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if h.angelus.Backend == nil {
		return nil, errors.New("import: no backend (ephemeral angelus)")
	}
	if req.Outfit == "" {
		return nil, errors.New("import: no outfit named")
	}
	outfitID, err := h.angelus.Backend.CreateOutfit(req.Outfit, req.OutfitPatch)
	if err != nil {
		return nil, fmt.Errorf("import: outfit %q: %w", req.Outfit, err)
	}
	id, err := h.angelus.Backend.CreateConversation(outfitID)
	if err != nil {
		return nil, fmt.Errorf("import: create conversation: %w", err)
	}
	log, err := h.angelus.Backend.Open(id)
	if err != nil {
		return nil, fmt.Errorf("import: open %q: %w", id, err)
	}
	for i, m := range req.Messages {
		if _, err := log.Append(store.Entry[message.Message]{Payload: m}); err != nil {
			return nil, fmt.Errorf("import: append message %d of %d: %w", i+1, len(req.Messages), err)
		}
	}
	// The chalkboard last, and as ONE patch: it is the aria's settled state,
	// not a history of how it got there. aria_id is re-stamped because the
	// exported board carries the id it had in the store it came from — the
	// same re-stamp a fork does, for the same reason.
	patch := req.Chalkboard
	if patch.Set == nil {
		patch.Set = map[string]json.RawMessage{}
	}
	if b, mErr := json.Marshal(id); mErr == nil {
		patch.Set["aria_id"] = b
	}
	if _, err := h.angelus.Backend.ApplyChalkboard(id, patch); err != nil {
		return nil, fmt.Errorf("import: chalkboard: %w", err)
	}
	// The list sidecar, so an imported aria is a first-class row in `figaro
	// ls` rather than an id with dashes after it. Derived from what actually
	// arrived; the token counts are the source's and are carried as history,
	// not as a claim about this store's spend.
	meta := &store.AriaMeta{
		MessageCount: len(req.Messages),
		LastActiveMS: time.Now().UnixMilli(),
		OutfitName:   req.Outfit,
		Mantra:       req.Mantra,
		Provider:     req.Provider,
		Model:        req.Model,
	}
	for _, m := range req.Messages {
		if m.Role == message.RoleOutput {
			meta.TurnCount++
		}
	}
	if err := h.angelus.Backend.SetMeta(id, meta); err != nil {
		slog.Warn("import: set meta", "id", id, "err", err)
	}
	h.angelus.Backend.Kick()
	slog.Info("imported aria", "id", id, "outfit", req.Outfit, "messages", len(req.Messages))
	return rpc.ImportResponse{
		FigaroID: id, Outfit: req.Outfit, Messages: len(req.Messages), WasID: req.WasID,
	}, nil
}

func (h *handlers) promote(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.PromoteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if h.angelus.Backend == nil {
		return nil, errors.New("promote: no backend (ephemeral angelus)")
	}
	climbed, err := h.angelus.Backend.Promote(req.FigaroID, req.Levels)
	if errors.Is(err, store.ErrNoTrunkCapability) {
		return rpc.PromoteResponse{FigaroID: req.FigaroID, Unsupported: true}, nil
	}
	if errors.Is(err, store.ErrAtStump) {
		return rpc.PromoteResponse{FigaroID: req.FigaroID, Climbed: 0, AtStump: true}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("promote %q: %w", req.FigaroID, err)
	}
	slog.Info("promoted figaro", "trunk", req.FigaroID, "levels", req.Levels, "climbed", climbed)
	return rpc.PromoteResponse{FigaroID: req.FigaroID, Climbed: climbed}, nil
}

// runtimeFillins returns the per-process boot keys the outfit can't
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
// plus the per-create req.Patch overrides. The outfit itself is NOT
// re-stated here — it is inherited via the fork watermark and rendered
// in the shared outfit-node prefix.
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

// bootPatchEphemeral is the ephemeral boot: the full resolved outfit
// (no channel to inherit from) plus runtime fill-ins. max_tokens
// defaults when the outfit omits it.
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
// outfit patch's system.* keys.
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

	// Kill is a deletion and dormancy is not: a removed aria takes its
	// background jobs with it. Unconditional, because a hibernated aria has
	// no live agent and still owns running children.
	if h.angelus.Sessions != nil {
		if n := h.angelus.Sessions.KillScope(req.FigaroID); n > 0 {
			slog.Info("killed aria sessions", "id", req.FigaroID, "sessions", n)
		}
	}

	if h.angelus.Backend != nil {
		if err := h.angelus.Backend.Remove(req.FigaroID, req.Recursive); err != nil {
			return nil, err // surface "has live branches" etc. to the caller
		}
	}

	// The endpoint outlives the AGENT, not the aria. A deleted aria has no
	// address, so the hub goes with it and connected clients get their EOF —
	// which is correct here and exactly what must not happen on hibernate.
	if hb := h.angelus.Hubs.drop(req.FigaroID); hb != nil {
		hb.Close()
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
	boundPIDs := h.angelus.Registry.BoundPIDsByFigaro()
	result := make([]rpc.FigaroInfoResponse, 0, len(live))
	enrichments := make([]listEnrichment, 0, len(live))
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
			ContextLimit:     info.ContextLimit,
			ContextExact:     info.ContextExact,
			CreatedAt:        info.CreatedAt.UnixMilli(),
			LastActive:       info.LastActive.UnixMilli(),
			Mantra:           info.Mantra,
			Cwd:              info.Cwd,
			OutfitName:       info.OutfitName,
			BoundPIDs:        boundPIDs[info.ID],
		}
		if !req.IDsOnly && info.OutfitName != "" {
			entry.OutfitVer = outfitVerLabel(info.OutfitVersion, h.currentOutfitHash(info.OutfitName))
		}
		result = append(result, entry)
	}

	// Snapshot the forest once per request. Ordinary lists need conversation
	// nodes only; global listings also need the ceremonial anchors. ID-only
	// completion skips vectors and anchors entirely.
	var nodeList []store.NodeView
	nodeByID := map[string]store.NodeView{}
	var conversationIDs []string
	if h.angelus.Backend != nil {
		switch {
		case req.IDsOnly && !req.Global:
			conversationIDs = h.angelus.Backend.ConversationIDs()
		case req.Global:
			nodeList = h.angelus.Backend.Nodes()
		default:
			nodeList = h.angelus.Backend.Conversations()
		}
		for _, n := range nodeList {
			if n.Kind == conversationKind {
				conversationIDs = append(conversationIDs, n.ID)
			}
			if !req.IDsOnly {
				nodeByID[n.ID] = n
			}
		}
	}

	// Dormant conversation trunks (not currently registered/live).
	for _, id := range conversationIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		entry := rpc.FigaroInfoResponse{ID: id, State: "dormant"}
		if req.IDsOnly {
			if meta, _ := h.angelus.Backend.Meta(id); meta != nil {
				entry.MessageCount = meta.MessageCount
				entry.TokensIn = meta.TokensIn
				entry.TokensOut = meta.TokensOut
				entry.CacheReadTokens = meta.CacheReadTokens
				entry.CacheWriteTokens = meta.CacheWriteTokens
				if meta.LastActiveMS != 0 {
					entry.LastActive = meta.LastActiveMS
				}
			}
		}
		result = append(result, entry)
		if !req.IDsOnly {
			enrichments = append(enrichments, listEnrichment{
				index:  len(result) - 1,
				ariaID: id,
			})
		}
	}

	// Global: also surface the ceremonial anchors — the null genesis trunk and
	// every versioned outfit — that the conversation filter above skips.
	// fillFromNode below stamps their Kind/Outfit/Version/Parent.
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
		h.enrichList(result, enrichments)
		for i := range result {
			h.fillFromNode(nodeByID, &result[i])
		}
	}

	return rpc.ListResponse{Figaros: result}, nil
}

func (h *handlers) enrichList(result []rpc.FigaroInfoResponse, tasks []listEnrichment) {
	if h.angelus.Backend == nil || len(tasks) == 0 {
		return
	}
	workers := min(8, len(tasks))
	queue := make(chan listEnrichment)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range queue {
				entry := &result[task.index]
				meta, _ := h.angelus.Backend.Meta(task.ariaID)
				if meta != nil {
					h.fillFromMeta(meta, entry)
				}
			}
		}()
	}
	for _, task := range tasks {
		queue <- task
	}
	close(queue)
	wg.Wait()
}

func (h *handlers) fillFromMeta(meta *store.AriaMeta, entry *rpc.FigaroInfoResponse) {
	entry.MessageCount = meta.MessageCount
	entry.TokensIn = meta.TokensIn
	entry.TokensOut = meta.TokensOut
	entry.CacheReadTokens = meta.CacheReadTokens
	entry.CacheWriteTokens = meta.CacheWriteTokens
	entry.ContextTokens = meta.ContextTokens
	entry.ContextLimit = meta.ContextLimit
	entry.ContextExact = meta.ContextExact
	entry.Provider = meta.Provider
	entry.Model = meta.Model
	entry.Mantra = meta.Mantra
	entry.Cwd = meta.Cwd
	entry.OutfitName = meta.OutfitName
	if meta.CreatedAtMS != 0 {
		entry.CreatedAt = meta.CreatedAtMS
	}
	if meta.LastActiveMS != 0 {
		entry.LastActive = meta.LastActiveMS
	}
	if meta.OutfitName != "" {
		entry.OutfitVer = outfitVerLabel(meta.OutfitVersion, h.currentOutfitHash(meta.OutfitName))
	}
}

// fillFromNode adds the fork-forest position (vector/trunk/parent/branched-at)
// from the tree. The forest is snapshotted by the caller (once per request)
// and indexed by id, so this is a map lookup.
func (h *handlers) fillFromNode(nodes map[string]store.NodeView, entry *rpc.FigaroInfoResponse) {
	n, ok := nodes[entry.ID]
	if !ok {
		return
	}
	entry.Vector = n.Vector
	entry.Trunk = n.Trunk
	entry.Parent = n.Parent
	entry.BranchedLT = n.BranchedLT
	entry.Kind = n.Kind
	// Ceremonial outfit anchors carry their name + a live/stale label here
	// (conversations get those from their chalkboard stamp instead).
	if n.Kind == string(outfitKind) {
		entry.OutfitName = n.Outfit
		entry.OutfitVer = outfitVerLabel(n.Version, h.currentOutfitHash(n.Outfit))
	}
}

// outfitKind / nullKind / conversationKind mirror the store's nodeKind string
// values (the store package's constants are unexported).
const (
	nullKind         = "null"
	outfitKind       = "outfit"
	conversationKind = "conversation"
)

// bind attends a shell to an aria WITHOUT waking it. Binding is an identity
// fact, so the only thing that must exist is the aria on disk and an endpoint
// to dial — never an agent. This is what makes `figaro attend` free.
func (h *handlers) bind(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.BindRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := h.requireAria(req.FigaroID); err != nil {
		return nil, fmt.Errorf("bind: %w", err)
	}
	// The endpoint must be listening before the caller is told the bind
	// succeeded: a unix socket cannot be lazily activated, so a client that
	// dials an unopened path gets ECONNREFUSED rather than a wakeup.
	if _, err := h.hubFor(req.FigaroID); err != nil {
		return nil, fmt.Errorf("bind: %w", err)
	}
	if err := h.angelus.Registry.Bind(req.PID, req.FigaroID, req.AtMainLT); err != nil {
		return nil, err
	}
	return rpc.BindResponse{OK: true}, nil
}

// requireAria proves an aria exists without constructing anything. A bad id
// must still be an error — attending a typo has to fail, not open a socket
// for a conversation that was never born.
func (h *handlers) requireAria(id string) error {
	if err := rpc.ValidateAriaID(id); err != nil {
		return err
	}
	if h.angelus.Registry.Get(id) != nil {
		return nil // live: already proven
	}
	if h.angelus.Backend == nil {
		return fmt.Errorf("aria %s: no backend (ephemeral angelus)", id)
	}
	// A topology lookup, not a read: Meta is NOT an existence check (it
	// returns nil,nil for an unknown aria, and arias predating the sidecar
	// legitimately have none — see backfill.go), and Open would decode the
	// whole IR just to prove the aria is there.
	if _, ok := h.angelus.Backend.Node(id); !ok {
		return fmt.Errorf("aria %s not found", id)
	}
	return nil
}

// attach opens an aria's endpoint without touching pid bindings and WITHOUT
// waking it. It used to restore; it now only guarantees somewhere to dial.
// The aria wakes on the first request that needs a turn loop, which for a
// transcript or a pager is never.
func (h *handlers) attach(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.AttachRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := h.requireAria(req.FigaroID); err != nil {
		return nil, fmt.Errorf("attach %s: %w", req.FigaroID, err)
	}
	hb, err := h.hubFor(req.FigaroID)
	if err != nil {
		return nil, fmt.Errorf("attach %s: %w", req.FigaroID, err)
	}
	return rpc.AttachResponse{
		FigaroID: req.FigaroID,
		Endpoint: rpc.Endpoint{Scheme: "unix", Address: hb.sockPath},
	}, nil
}

func (h *handlers) resolve(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ResolveRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	// A dormant aria is a FOUND aria. The binding is the answer and the
	// address is a pure function of the id, so nothing here needs an agent —
	// resolving must not be the thing that wakes what the sweep reclaimed.
	id, _, lt := h.angelus.Registry.Resolve(req.PID)
	if id == "" {
		return rpc.ResolveResponse{Found: false}, nil
	}
	hb, err := h.hubFor(id)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", id, err)
	}
	return rpc.ResolveResponse{
		FigaroID: id,
		Endpoint: rpc.Endpoint{Scheme: "unix", Address: hb.sockPath},
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
		Build:       h.angelus.Build,
		Mem:         h.angelus.MemStatus(),
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

	// The backend returns the same shared, memoized IR instance the live
	// agent holds, so reads run lock-free against its writes.
	log, err := h.angelus.Backend.Open(req.FigaroID)
	if err != nil {
		return nil, fmt.Errorf("aria.read: open: %w", err)
	}

	limit := req.Limit
	if limit <= 0 || limit > ariaReadHardCap {
		limit = ariaReadHardCap
	}

	// Keyset pagination: Before takes precedence over From.
	if req.Before > 0 {
		selected, total := log.ReadPage(0, req.Before, limit)
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

	selected, total := log.ReadPage(req.From, 0, limit+1)
	page := selected
	if len(page) > limit {
		page = page[:limit]
	}
	out := make([]rpc.AriaReadEntry, 0, len(page))
	for _, e := range page {
		raw, mErr := json.Marshal(e.Payload)
		if mErr != nil {
			return nil, fmt.Errorf("aria.read: marshal LT=%d: %w", e.LT, mErr)
		}
		out = append(out, rpc.AriaReadEntry{LT: e.LT, Payload: raw})
	}
	var nextFrom uint64
	if len(selected) > limit {
		nextFrom = selected[limit].LT
	}
	return rpc.AriaReadResponse{
		Entries:  out,
		Total:    total,
		NextFrom: nextFrom,
	}, nil
}

// restoreByID re-creates a figaro from the backend tree. Serialized per
// aria so concurrent restores cannot double-replay tail repair.
func (h *handlers) restoreByID(ctx context.Context, ariaID string) (figaro.Figaro, error) {
	if f := h.angelus.Registry.Get(ariaID); f != nil {
		return f, nil
	}
	if h.angelus.Backend == nil {
		return nil, fmt.Errorf("no backend configured")
	}
	mu := h.restoreLock(ariaID)
	mu.Lock()
	defer mu.Unlock()
	if f := h.angelus.Registry.Get(ariaID); f != nil {
		return f, nil
	}
	return h.restoreOne(ctx, ariaID)
}

func (h *handlers) restoreLock(ariaID string) *sync.Mutex {
	h.restoreMu.Lock()
	defer h.restoreMu.Unlock()
	if h.restoreLocks == nil {
		h.restoreLocks = map[string]*sync.Mutex{}
	}
	mu, ok := h.restoreLocks[ariaID]
	if !ok {
		mu = &sync.Mutex{}
		h.restoreLocks[ariaID] = mu
	}
	return mu
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
		if meta.LastActiveMS != 0 {
			lastActive = time.UnixMilli(meta.LastActiveMS)
		}
	}
	reg := tool.DefaultRegistryForAria(ariaID, cwdFromChalkboard(cb, toolRoot),
		tool.WithImageBudget(h.config.InlineImageBudget()),
		tool.WithSessions(h.angelus.Sessions))
	agent := figaro.NewAgent(figaro.Config{
		ID:              ariaID,
		SocketPath:      sockPath,
		Provider:        prov,
		ProviderFactory: h.factory,
		Outfitter:       h.outfitter,
		Tools:           reg,
		Projector:       uiir.New(reg),
		Backend:         h.angelus.Backend,
		Chalkboard:      cb,
		CreatedAt:       createdAt,
		LastActive:      lastActive,
		Settings:        h.config,
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

// errNoDefaultOutfit builds a typed JSON-RPC error directing the
// client to drive first-run outfit selection.
func (h *handlers) errNoDefaultOutfit() error {
	data, _ := json.Marshal(rpc.ErrorData{AvailableProviders: h.availableProviders})
	return &jkrpc.Error{
		Code:    rpc.ErrNoDefaultOutfit,
		Message: "no default outfit configured",
		Data:    data,
	}
}

// errNoProvider builds a typed JSON-RPC error indicating the
// resolved outfit has no system.provider key.
func (h *handlers) errNoProvider(outfitName string) error {
	data, _ := json.Marshal(rpc.ErrorData{
		AvailableProviders: h.availableProviders,
		Outfit:             outfitName,
	})
	return &jkrpc.Error{
		Code:    rpc.ErrNoProvider,
		Message: fmt.Sprintf("outfit %q has no system.provider", outfitName),
		Data:    data,
	}
}

// errOutfitNotFound builds a typed JSON-RPC error for a missing
// named outfit. cause carries the underlying outfit error, and when that names
// a broken layer reference the whole closure travels with it, so the caller can
// draw where the gap is.
func (h *handlers) errOutfitNotFound(name string, cause error) error {
	payload := rpc.ErrorData{
		Name:        name,
		SearchPaths: []string{h.config.OutfitPath(name)},
	}
	var missing *outfit.MissingError
	if errors.As(cause, &missing) {
		payload.OutfitClosure = figaro.OutfitClosureWire(missing.Closure)
	}
	data, _ := json.Marshal(payload)
	return &jkrpc.Error{
		Code:    rpc.ErrOutfitNotFound,
		Message: fmt.Sprintf("outfit %q not found: %s", name, cause),
		Data:    data,
	}
}
