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
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/outfit"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/turns"
	"github.com/jack-work/figaro/internal/uiir"
	"github.com/jack-work/jkrpc"
)

// ProviderFactory creates providers for an Agent; instances never span
// arias. The agent keeps the factory so it can rebind mid-conversation when
// system.provider changes on the form (see figaro.syncProvider).
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

	// FormTemplates renders Patches as system reminders. nil = skip.
	FormTemplates *template.Template
}

// Handlers wraps the angelus JSON-RPC handler map.
type Handlers struct {
	Map map[string]jkrpc.HandlerFunc
	h   *handlers
}

// noticeUpgrade marks the default form for recomputation when the BINARY's
// bundled skills have moved since it was minted.
//
// Without this, `nix profile upgrade` ships new first-party skills that no
// aria ever wears. The default-form pointer is reused with no comparison
// while it is clean (that reuse is what shares the rendered prefix in the
// provider's cache, so it is not a shortcut to give up), and only `fig outfit
// reload` ever set the flag. A user who upgrades and never runs that verb
// keeps minting arias against the skills of the build they replaced.
//
// The trigger is the bundled root path, which carries the store hash and so
// moves on every upgrade. It only sets the FLAG: whether anything is reminted
// is still decided by the hash comparison in ensureDefaultForm, so a rebuild
// with identical skills costs one comparison and keeps the same form.
func (h *handlers) noticeUpgrade() {
	b := h.angelus.Backend
	if b == nil {
		return
	}
	rec, err := b.LoadDefaultForm()
	if err != nil || rec == nil {
		return
	}
	root := outfit.BundledSkillsRoot()
	if rec.BundledRoot == root {
		return
	}
	was := rec.BundledRoot
	rec.BundledRoot = root
	rec.Dirty = true
	if err := b.SaveDefaultForm(rec); err != nil {
		slog.Warn("default form: could not record the bundled skills root", "err", err)
		return
	}
	slog.Info("bundled skills moved: the default form will be recomputed on the next new",
		"was", was, "now", root, "form", rec.FormID)
}

// newOutfitter builds the daemon's ONE resolver, with its snapshot store in
// the runtime directory. Snapshots are what keep a resolution from straddling
// an edit: the first read of a file in an epoch pins its bytes, and everything
// derived in that epoch: including a fold rebuilt after eviction: is derived
// from the pinned copy. Runtime-scoped on purpose: the guarantee they provide
// is per-daemon-run, and a reboot has nothing to be consistent with.
func newOutfitter(a *Angelus, loaded *config.Loaded) *outfit.Outfitter {
	dir := ""
	if loaded != nil {
		dir = loaded.ConfigDir
	}
	snap := ""
	if a != nil && a.RuntimeDir != "" {
		snap = filepath.Join(a.RuntimeDir, "outfit-snapshots")
	}
	return outfit.NewAt(dir, snap)
}

// NewHandlers creates the handler set for the angelus socket.
func NewHandlers(cfg ServerConfig) *Handlers {
	h := &handlers{
		angelus:            cfg.Angelus,
		config:             cfg.Config,
		factory:            cfg.ProviderFactory,
		ctx:                cfg.Ctx,
		formTmpls:          cfg.FormTemplates,
		outfitter:          newOutfitter(cfg.Angelus, cfg.Config),
		availableProviders: cfg.AvailableProviders,
	}
	// Warm the ONE closure `fig new` is certain to want, in the background.
	// Nothing blocks on it: startup reads no outfit file, and every other name
	// is folded when someone asks for it.
	if cfg.Config != nil {
		h.outfitter.Warm(cfg.Config.Config.DefaultOutfit)
	}
	h.noticeUpgrade()
	return &Handlers{
		Map: authz.Guard(map[string]jkrpc.HandlerFunc{
			rpc.MethodCreate:       h.create,
			rpc.MethodFormCreate:   h.formCreate,
			rpc.MethodFormBind:     h.formBind,
			rpc.MethodOutfitReload: h.outfitReload,
			rpc.MethodFork:         h.fork,
			rpc.MethodPromote:      h.promote,
			rpc.MethodImport:       h.importAria,
			rpc.MethodOutfits:      h.outfits,
			rpc.MethodConfigure:    h.configure,
			rpc.MethodNormalize:    h.normalize,
			rpc.MethodGC:           h.gc,
			rpc.MethodKill:         h.kill,
			rpc.MethodList:         h.list,
			rpc.MethodAttach:       h.attach,
			rpc.MethodBind:         h.bind,
			rpc.MethodResolve:      h.resolve,
			rpc.MethodUnbind:       h.unbind,
			rpc.MethodStatus:       h.status,
			rpc.MethodSaveBindings: h.saveBindings,
			rpc.MethodAriaRead:     h.ariaRead,
			rpc.MethodAriaPage:     h.ariaPage,
			rpc.MethodAriaContext:  h.ariaContext,
			rpc.MethodAriaForm:     h.ariaForm,
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
	formTmpls          *template.Template
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

// settings is the in-memory config and the outfitter over it, read under the
// lock. Config is loaded once at start and re-read only when a client changes
// it through MethodConfigure: the first-run wizard's seam: so no request pays
// a stat, and the two fields can never be observed half-swapped.
func (h *handlers) settings() (*config.Loaded, *outfit.Outfitter) {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	return h.config, h.outfitter
}

// openAriaForm returns the in-memory form hot view for an
// aria, seeded from its reducible form channel (the durable
// truth: there is no the form channel). nil on failure.
func (h *handlers) openAriaForm(ariaID string) *form.State {
	if h.formTmpls == nil || h.angelus.Backend == nil {
		return nil
	}
	snap, err := h.angelus.Backend.FormState(ariaID)
	if err != nil {
		slog.Warn("form state (disabled for aria)", "aria", ariaID, "err", err)
		return nil
	}
	st, _ := form.Open("")
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
// is a stat per dependency: cheaper than the TTL was, and never stale.
func (h *handlers) currentOutfitHash(name string) (current, legacy string) {
	_, ofit := h.settings()
	if ofit == nil {
		return "", ""
	}
	names, perr := outfit.TermNames(name)
	if perr != nil || len(names) == 0 {
		return "", "" // a stamp carrying a literal is not re-resolvable
	}
	p, err := ofit.Names(names...)
	if err != nil {
		return "", ""
	}
	current, _ = store.OutfitVersion(name, p)
	legacy, _ = store.LegacyOutfitVersion(p)
	return current, legacy
}

// outfitVer is the version column for one row: re-resolve the outfit named by
// the stamp, and compare.
//
// A listing asks this once per ROW and the answer only depends on the outfit,
// so it carries a per-request memo. Without one, a store of 200 arias on one
// outfit re-marshalled and re-hashed that outfit's whole patch: skills and
// all: 200 times, and twice each since the legacy generation joined the
// comparison. The fold underneath is cached; the hashing was not.
func (h *handlers) outfitVer(vers map[string][2]string, stamped, name string) string {
	hashes, ok := vers[name]
	if !ok {
		current, legacy := h.currentOutfitHash(name)
		hashes = [2]string{current, legacy}
		if vers != nil {
			vers[name] = hashes
		}
	}
	return outfitVerLabel(stamped, hashes[0], hashes[1])
}

// outfitVerLabel renders the version column: "live" when the stamped hash
// matches the current one, else the stamped hash's first 8 chars.
//
// legacy is the same fold hashed the pre-name way. An aria minted by an older
// build carries that stamp and its outfit has not changed, so calling it stale
// would be a lie told by a hash input, not by an outfit. Goes when it can.
func outfitVerLabel(stamped, current, legacy string) string {
	if stamped == "" {
		return ""
	}
	if stamped == current || (legacy != "" && stamped == legacy) {
		return "live"
	}
	if len(stamped) > 8 {
		return stamped[:8]
	}
	return stamped
}

// formCreate mints an unbound form: the form half of the one birth verb.
// No outfit resolution, no default, no dedup: the patch IS the form, and
// naming an outfit is the CLIENT's affair (the CLI materializes -O into
// the patch before calling; a form with no patch is refused by the store).
// The hub is stood up before the response so the @id is dialable the
// moment the caller holds it: same rule as every endpoint: unix sockets
// have no lazy activation.
func (h *handlers) formCreate(ctx context.Context, params json.RawMessage) (interface{}, error) {
	_, span := figOtel.Start(ctx, "angelus.formCreate")
	defer span.End()

	if h.angelus.Backend == nil {
		return nil, fmt.Errorf("form.create: no backend (ephemeral angelus)")
	}
	var req rpc.FormCreateRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if req.Patch == nil || req.Patch.IsEmpty() {
		return nil, fmt.Errorf("form.create: a form is born of its patch; an empty one names nothing")
	}
	// Dress at the boundary: the request's outfit NAMES fold into keys here,
	// under the patch's own, and the form is born holding materialized state.
	// No default outfit is ever folded in: `fig form new` demands its names
	// explicitly.
	patch, err := h.dress(req.Outfits, req.Patch)
	if err != nil {
		return nil, err
	}
	id, version, err := h.angelus.Backend.CreateForm(req.Parent, patch)
	if err != nil {
		return nil, err
	}
	hb, err := h.hubFor(id)
	if err != nil {
		return nil, fmt.Errorf("form.create: %s minted but endpoint failed: %w", id, err)
	}
	return rpc.FormCreateResponse{
		FormID:   id,
		Version:  version,
		Endpoint: rpc.Endpoint{Scheme: "unix", Address: hb.sockPath},
	}, nil
}

// formBind births a figaro from an unbound form (or the null root): the
// bind half of the one birth verb. It mirrors create's storage dance -
// fork with the dressing, stamp aria_id in the boot patch, and then
// STOPS: no provider is resolved, no agent constructed, no registry
// entry. The figaro is born dormant behind its hub and wakes on first
// need; a missing provider fails there, at the first turn, which is what
// makes `bind null` a mintable naked figaro. It never touches the
// caller's attendance either: binding shells is the client's affair.
func (h *handlers) formBind(ctx context.Context, params json.RawMessage) (interface{}, error) {
	_, span := figOtel.Start(ctx, "angelus.formBind")
	defer span.End()

	if h.angelus.Backend == nil {
		return nil, fmt.Errorf("form.bind: no backend (ephemeral angelus)")
	}
	var req rpc.FormBindRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	parent := req.Parent
	if parent == "null" {
		parent = ""
	}
	if parent != "" {
		n, ok := h.angelus.Backend.Node(parent)
		isStump := ok && n.Kind == "outfit"
		if !ok || (n.Kind != store.KindForm && !isStump) {
			return nil, fmt.Errorf("form.bind: %s is not an unbound form (bind forks forms; a figaro forks with `fig fork`)", req.Parent)
		}
	}
	dress, err := h.dress(req.Outfits, req.Patch)
	if err != nil {
		return nil, err
	}
	cwd, _ := os.Getwd()
	id, _, err := h.angelus.Backend.ForkWith(parent, 0, childBirthPatch(dress, cwd))
	if err != nil {
		return nil, fmt.Errorf("form.bind: mint figaro: %w", err)
	}
	if _, err := h.angelus.Backend.ApplyForm(id, convBootPatch(id, cwd)); err != nil {
		return nil, fmt.Errorf("form.bind: stamp aria id: %w", err)
	}
	hb, err := h.hubFor(id)
	if err != nil {
		return nil, fmt.Errorf("form.bind: %s minted but endpoint failed: %w", id, err)
	}
	return rpc.FormBindResponse{
		FigaroID: id,
		Endpoint: rpc.Endpoint{Scheme: "unix", Address: hb.sockPath},
	}, nil
}

// ensureDefaultForm returns the id of the current default form, minting a
// fresh one when due. The lifecycle (v2 brief §6): `fig outfit reload`
// only sets a dirty flag; the compute happens HERE, on the next fig new -
// materialized files are hashed and compared against the record, and a
// pointer that is clean and whose node still exists is reused with NO
// comparison at all (the cheap path, and the prompt-cache-preserving one:
// reuse of the same node is what shares the rendered prefix). A remint is
// due when: no record, dirty + hash moved, or dirty + the form was patched
// by hand since birth (propagating an ad-hoc patch to every future aria is
// exactly what the dirty-compute refuses to do silently).
func (h *handlers) ensureDefaultForm(backend store.Backend, stumpPatch form.Patch, outfitName string) (string, error) {
	rec, err := backend.LoadDefaultForm()
	if err != nil {
		return "", fmt.Errorf("default form record: %w", err)
	}
	if rec != nil {
		if _, ok := backend.Node(rec.FormID); !ok {
			rec = nil // the form was removed; remint
		}
	}
	if rec != nil && !rec.Dirty {
		return rec.FormID, nil
	}
	birth := birthPatch(stumpPatch, outfitName, "")
	hash, err := store.ContentVersion(birth)
	if err != nil {
		return "", fmt.Errorf("default form hash: %w", err)
	}
	if rec != nil && rec.Dirty && rec.BirthHash == hash {
		if v, verr := backend.FormVersion(rec.FormID); verr == nil && v == rec.BirthVersion {
			rec.Dirty = false // same files, untouched form: reload is a no-op
			if err := backend.SaveDefaultForm(rec); err != nil {
				return "", err
			}
			return rec.FormID, nil
		}
	}
	id, version, err := backend.CreateForm("", birth)
	if err != nil {
		return "", fmt.Errorf("mint default form: %w", err)
	}
	if err := backend.SaveDefaultForm(&store.DefaultFormRecord{
		FormID: id, BirthHash: hash, BirthVersion: version,
		BundledRoot: outfit.BundledSkillsRoot(),
	}); err != nil {
		return "", err
	}
	slog.Info("default form minted", "form", id, "outfit", outfitName, "hash", hash)
	return id, nil
}

// outfitReload flags the default form for recomputation on the next
// `fig new`. Deliberately cheap: no files are read here, and there is NO
// inverse verb: outfit files are one-way sources of truth.
func (h *handlers) outfitReload(ctx context.Context, params json.RawMessage) (interface{}, error) {
	// Turn the resolver's epoch over first: whatever else this verb decides,
	// asking for a reload means the files on disk are the truth now. It reads
	// nothing: the next fold does the reading: which is what keeps this the
	// cheap verb §6 of the brief says it is.
	if _, ofit := h.settings(); ofit != nil {
		ofit.Reload()
	}
	if h.angelus.Backend == nil {
		return nil, fmt.Errorf("outfit.reload: no backend (ephemeral angelus)")
	}
	rec, err := h.angelus.Backend.LoadDefaultForm()
	if err != nil {
		return nil, err
	}
	if rec == nil {
		// Nothing minted yet: the next fig new computes from files anyway.
		return rpc.OutfitReloadResponse{}, nil
	}
	rec.Dirty = true
	if err := h.angelus.Backend.SaveDefaultForm(rec); err != nil {
		return nil, err
	}
	return rpc.OutfitReloadResponse{Flagged: true, FormID: rec.FormID}, nil
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
	loaded, _ := h.settings()

	// TWO PATCHES, and which is which is the whole economy of this.
	//
	// The STUMP carries the default outfit's closure and NOTHING else, so its
	// identity is a pure function of that closure: every aria on the same
	// outfit shares one node, one set of records, and one rendered prefix in
	// the provider's cache. Folding the caller's -O in here (which is what
	// 0.22.1 did) minted a private stump per literal, so `-O mantra=x` defeated
	// the sharing this exists for.
	//
	// The CHILD carries everything per-aria: what -O asked for, the runtime
	// fill-ins, its own id. So `-O` adds to the default instead of replacing
	// it, and that rule now falls out of the topology rather than being
	// arranged: the default is what the child inherits, -O is what it wrote.
	// Resolved through the reserved `default` layer, which is the one LENIENT
	// name: a configured default that is not on disk yet folds to nothing
	// rather than failing, because that absence is what the first-run flow
	// rides on: it surfaces downstream as the missing provider it is.
	outfitName := loaded.Config.DefaultOutfit
	stumpPatch, err := h.dressDefault()
	if err != nil {
		return nil, err
	}

	dress, err := h.dress(req.Outfits, req.Patch)
	if err != nil {
		return nil, err
	}
	// The provider is resolved against what the aria will ACTUALLY wear: the
	// stump underneath, the dressing on top.
	base := mergePatches(stumpPatch, dress)
	// Nothing configured and nothing sent: there is no first move to make. A
	// default that IS named but missing on disk falls through, so the failure
	// is reported as the missing provider it actually is.
	if base.IsEmpty() && outfitName == "" {
		return nil, h.errNoDefaultOutfit()
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

	// The form channel is the durable truth; cbState is the
	// in-memory hot view (no the form channel). System mints all ids.
	cbState, _ := form.Open("")
	var id string
	var inlineBoot *form.Patch

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
		// Ensure the DEFAULT FORM (stumps are legacy), then fork it: `fig
		// new` is bind-the-default-form, and this reuse is what shares one
		// rendered prefix, and one warm provider cache, across every
		// aria on the same outfit. The hash optimization IS the cache.
		formID, serr := h.ensureDefaultForm(backend, stumpPatch, outfitName)
		if serr != nil {
			return nil, serr
		}
		var cerr error
		id, _, cerr = backend.ForkWith(formID, 0, childBirthPatch(dress, cwd))
		if cerr != nil {
			return nil, fmt.Errorf("mint aria: %w", cerr)
		}
		// aria_id can only be stamped once the id exists, so it rides a second
		// patch rather than the birth one: which keeps the birth patch a pure
		// function of what was asked for, and so a stable identity.
		if _, aerr := backend.ApplyForm(id, convBootPatch(id, cwd)); aerr != nil {
			return nil, fmt.Errorf("stamp aria id: %w", aerr)
		}
		snap, serr := backend.FormState(id)
		if serr != nil {
			return nil, fmt.Errorf("read conversation form: %w", serr)
		}
		cbState.Apply(snap.AsPatch())
	}

	sockPath := filepath.Join(h.angelus.FigaroSocketDir(), id+".sock")

	reg := tool.DefaultRegistryForAria(id, cwdFromForm(cbState, cwd),
		tool.WithImageBudget(loaded.InlineImageBudget()),
		tool.WithSessions(h.angelus.Sessions))
	agent := figaro.NewAgent(figaro.Config{
		ID:              id,
		SocketPath:      sockPath,
		Provider:        prov,
		ProviderFactory: h.factory,
		Tools:           reg,
		Projector:       uiir.New(reg),
		Backend:         backend,
		Form:            cbState,
		InlineBoot:      inlineBoot,
		Settings:        loaded,
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
	// Dress the child in the call that mints it, on the alternative and after
	// the fork: a patch sent to the parent first can be ACKed and still miss
	// the branch.
	dress, err := h.dress(req.Outfits, req.Patch)
	if err != nil {
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
				note = fmt.Sprintf("%s is the genesis root: spawned a fresh outfitless conversation there", where)
			case owner.Outfit != "":
				note = fmt.Sprintf("%s is in outfit %s: spawned a fresh conversation under it", where, owner.Outfit)
			case owner.Trunk != "" && owner.Trunk != req.FigaroID:
				note = fmt.Sprintf("%s lives in trunk %s: branching there", where, owner.Trunk)
			}
		}
	}
	runFork := func() error {
		parentMeta := h.forkMetaSnapshot(req.FigaroID)
		// One critical section: the branch and the patch that dresses it. A
		// patch sent afterwards can be ACKed on the parent and miss the child;
		// a patch sent before has no child to land on.
		at := uint64(0)
		if interior {
			at = atMainLT
		}
		var ferr error
		alt, _, ferr = h.angelus.Backend.ForkWith(req.FigaroID, at, forkDress(dress, req.FigaroID))
		if ferr != nil {
			return ferr
		}
		cont = req.FigaroID
		h.seedForkMeta(parentMeta, req.FigaroID, alt, atMainLT, interior, forkOwner)
		// The child inherited its parent's aria_id with everything else; it
		// learns its own here, as a normal transition it sees on its next turn.
		if _, aerr := h.angelus.Backend.ApplyForm(alt, withAriaID(form.Patch{}, alt)); aerr != nil {
			slog.Warn("fork: stamp child aria id", "alt", alt, "err", aerr)
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
	if err := runFork(); err != nil {
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
	copy.CreatedAtMS = time.Now().UnixMilli()
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
// Collecting loses nothing: the next aria wanting that outfit re-mints the
// same id: so the only question is whether anything is still under it, which
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
// appended through the ordinary path. Every identity: node id, fork base, LT
// : is minted by THIS store, which is why an import can never collide with
// what is already here and never needs a renumbering pass.
//
// What it deliberately does not carry: the provider translation caches. They
// are a derivable wire cache, and the price of dropping them is one cache-miss
// on the next turn (which, per the anthropic assembler, replays without
// thinking blocks rather than with unsigned ones). Exactness is the graft's
// job: see proposals/aria-graft.md: not this one's.
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
	// The form last, and as ONE patch: it is the aria's settled state,
	// not a history of how it got there. aria_id is re-stamped because the
	// exported board carries the id it had in the store it came from: the
	// same re-stamp a fork does, for the same reason.
	patch := req.Form
	if patch.Set == nil {
		patch.Set = map[string]json.RawMessage{}
	}
	if b, mErr := json.Marshal(id); mErr == nil {
		patch.Set["aria_id"] = b
	}
	if _, err := h.angelus.Backend.ApplyForm(id, patch); err != nil {
		return nil, fmt.Errorf("import: form: %w", err)
	}
	// The list sidecar, so an imported aria is a first-class row in `figaro
	// ls` rather than an id with dashes after it. Derived from what actually
	// arrived; the token counts are the source's and are carried as history,
	// not as a claim about this store's spend.
	meta := &store.AriaMeta{
		MessageCount: len(req.Messages),
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
// birthPatch is what an aria is born carrying: the materialized outfit, the name
// it answers to, and the content hash of both. The hash covers the patch minus
// itself: it cannot cover its own value, and the NAME is inside it, so two
// outfits with identical bodies and different names stay two identities.
// forkDress is what a branch is born carrying. The dressing may be empty, a
// plain `fig fork` asks for nothing: but the patch may not be: the child
// inherits its parent's form, aria_id included, and an aria that answers to its
// parent's id cannot fork itself. The re-stamp is the floor.
func forkDress(dress form.Patch, parent string) form.Patch {
	p := form.Patch{Set: map[string]json.RawMessage{}, Remove: dress.Remove}
	for k, v := range dress.Set {
		p.Set[k] = v
	}
	// A placeholder the writer replaces would be a lie; the id is not known
	// until the child exists, so aria_id is re-stamped by the boot patch that
	// follows. What this guarantees is that the birth patch is never empty.
	p.Set["system.forked_from"] = json.RawMessage(`"` + parent + `"`)
	return p
}

// mergePatches folds b over a.
func mergePatches(a, b form.Patch) form.Patch {
	out := form.Patch{Set: map[string]json.RawMessage{}, Remove: append(append([]string(nil), a.Remove...), b.Remove...)}
	for k, v := range a.Set {
		out.Set[k] = v
	}
	for k, v := range b.Set {
		out.Set[k] = v
	}
	return out
}

// childBirthPatch is what an aria writes for ITSELF: the dressing it asked for
// and the runtime fill-ins. Everything else it inherits from the stump. It is
// never empty: cwd is always known: which is what lets ForkWith demand a
// patch.
func childBirthPatch(dress form.Patch, cwd string) form.Patch {
	p := form.Patch{Set: map[string]json.RawMessage{}, Remove: dress.Remove}
	for k, v := range dress.Set {
		p.Set[k] = v
	}
	if b, err := json.Marshal(cwd); err == nil && cwd != "" {
		p.Set["system.cwd"] = b
		p.Set["system.root"] = b
	}
	return p
}

func birthPatch(outfitPatch form.Patch, outfitName, cwd string) form.Patch {
	p := form.Patch{Set: map[string]json.RawMessage{}, Remove: outfitPatch.Remove}
	for k, v := range outfitPatch.Set {
		p.Set[k] = v
	}
	if b, err := json.Marshal(outfitName); err == nil && outfitName != "" {
		p.Set["system.outfit_name"] = b
	}
	if ver, err := store.ContentVersion(p); err == nil {
		if b, mErr := json.Marshal(ver); mErr == nil {
			p.Set["system.outfit_version"] = b
		}
	}
	// cwd rides the birth patch so the very first turn resolves tools against
	// the right root; aria_id cannot, because the id does not exist yet.
	if b, err := json.Marshal(cwd); err == nil && cwd != "" {
		p.Set["system.cwd"] = b
		p.Set["system.root"] = b
	}
	return p
}

func runtimeFillins(ariaID, cwd string) form.Patch {
	p := form.Patch{Set: map[string]json.RawMessage{}}
	if b, err := json.Marshal(ariaID); err == nil && ariaID != "" {
		p.Set["aria_id"] = b
	}
	if b, err := json.Marshal(cwd); err == nil {
		p.Set["system.cwd"] = b
		p.Set["system.root"] = b
	}
	if env := form.EnvironmentPatch(); !env.IsEmpty() {
		for k, v := range env.Set {
			p.Set[k] = v
		}
	}
	return p
}

// convBootPatch is the conversation's boot transition: the runtime fill-ins,
// and nothing else. What the caller asked for is already in the birth patch -
// inherited through the fork watermark and rendered once in the shared prefix.
//
// It used to re-state the request here too, which is how the `layers` directive
// reached a board: the birth patch was materialized, this copy was not.
func convBootPatch(ariaID, cwd string) form.Patch {
	return runtimeFillins(ariaID, cwd)
}

// bootPatchEphemeral is the ephemeral boot: the full resolved outfit
// (no channel to inherit from) plus runtime fill-ins. max_tokens
// defaults when the outfit omits it.
func bootPatchEphemeral(base form.Patch, ariaID, cwd string) form.Patch {
	p := form.Patch{Set: map[string]json.RawMessage{}}
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
func withAriaID(p form.Patch, ariaID string) form.Patch {
	if b, err := json.Marshal(ariaID); err == nil {
		if p.Set == nil {
			p.Set = map[string]json.RawMessage{}
		}
		p.Set["aria_id"] = b
	}
	return p
}

// patchString reads a string value from a form.Patch's Set map.
func patchString(p form.Patch, key string) string {
	raw, ok := p.Set[key]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// patchInt reads an int value from a form.Patch's Set map.
func patchInt(p form.Patch, key string) int {
	raw, ok := p.Set[key]
	if !ok {
		return 0
	}
	var n int
	_ = json.Unmarshal(raw, &n)
	return n
}

// patchBool reads a bool value from a form.Patch's Set map.
func patchBool(p form.Patch, key string) bool {
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
func knobsFromPatch(p form.Patch) providerPkg.Knobs {
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
	// address, so the hub goes with it and connected clients get their EOF -
	// which is correct here and exactly what must not happen on hibernate.
	if hb := h.angelus.Hubs.drop(req.FigaroID); hb != nil {
		hb.Close()
	}

	slog.Info("killed figaro", "id", req.FigaroID)
	return rpc.KillResponse{OK: true}, nil
}

// list merges live and dormant arias.
func (h *handlers) list(ctx context.Context, params json.RawMessage) (interface{}, error) {
	// IDsOnly skips the per-aria form + node fills (the slow part): used
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
			BoundPIDs:        boundPIDs[info.ID],
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
			// Forms complete everywhere an id does: attend, kill, set --id.
			for _, f := range h.angelus.Backend.Forms() {
				conversationIDs = append(conversationIDs, f.ID)
			}
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
		entry := rpc.FigaroInfoResponse{ID: id, State: "dormant", BoundPIDs: boundPIDs[id]}
		if req.IDsOnly {
			if meta, _ := h.angelus.Backend.Meta(id); meta != nil {
				entry.MessageCount = meta.MessageCount
				entry.TokensIn = meta.TokensIn
				entry.TokensOut = meta.TokensOut
				entry.CacheReadTokens = meta.CacheReadTokens
				entry.CacheWriteTokens = meta.CacheWriteTokens
			}
			// Recency comes from figwal, not the sidecar: the newest record
			// timestamp anywhere in the node, read from the store WITHOUT
			// waking anything.
			entry.LastActive = h.angelus.Backend.LastTS(id)
		}
		result = append(result, entry)
		if !req.IDsOnly {
			enrichments = append(enrichments, listEnrichment{
				index:  len(result) - 1,
				ariaID: id,
			})
		}
	}

	// Global: also surface the ceremonial anchors: the null genesis trunk and
	// every versioned outfit: that the conversation filter above skips.
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
			entry := rpc.FigaroInfoResponse{ID: n.ID, State: "anchor", BoundPIDs: boundPIDs[n.ID]}
			if n.Kind == string(store.KindForm) {
				// A form is a live row, not a ceremonial anchor: it has
				// recency (figwal LastTS, wake-free, a form has nothing to
				// wake) and possibly a name and a casting (role) target,
				// both read from its folded state. Forms are few; the fold
				// is the same one `fig form <id>` performs.
				entry.State = "form"
				entry.LastActive = h.angelus.Backend.LastTS(n.ID)
				if snap, err := h.angelus.Backend.FormState(n.ID); err == nil {
					if v := snap.Lookup("name"); v != nil {
						entry.Name = *v
					}
					if v := snap.Lookup("target-aria"); v != nil {
						entry.TargetAria = *v
					}
				}
			}
			result = append(result, entry)
		}
	}

	// Forest position for every entry (live + dormant), from the snapshot -
	// and the outfit columns, which come from the stump and from nowhere else.
	// vers memoizes the re-resolve per outfit; it lives here because this pass
	// is single-threaded, unlike the metadata fill above.
	if !req.IDsOnly {
		h.enrichList(result, enrichments)
		vers := map[string][2]string{}
		for i := range result {
			h.fillFromNode(nodeByID, vers, &result[i])
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
				// Recency from figwal, sidecar-free, wake-free.
				entry.LastActive = h.angelus.Backend.LastTS(task.ariaID)
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
	if meta.CreatedAtMS != 0 {
		entry.CreatedAt = meta.CreatedAtMS
	}
}

// fillFromNode adds the fork-forest position (vector/trunk/parent/branched-at)
// from the tree. The forest is snapshotted by the caller (once per request)
// and indexed by id, so this is a map lookup.
func (h *handlers) fillFromNode(nodes map[string]store.NodeView, vers map[string][2]string, entry *rpc.FigaroInfoResponse) {
	n, ok := nodes[entry.ID]
	if !ok {
		return
	}
	entry.Vector = n.Vector
	entry.Trunk = n.Trunk
	entry.Parent = n.Parent
	entry.BranchedLT = n.BranchedLT
	entry.Kind = n.Kind
	if n.Kind == string(outfitKind) {
		entry.OutfitName = n.Outfit
		entry.OutfitVer = h.outfitVer(vers, n.Version, n.Outfit)
		return
	}
	// A conversation's outfit is the stump it was BORN under, carried down the
	// lineage by the topology walk -- not `system.outfit_name`, which is the
	// agent's own mutable copy, and not the presentation parent, which a
	// promote moves. Both were live lies: one `set` renamed an aria's outfit in
	// every listing and, because this column is what the version is
	// re-resolved against, reported an unchanged outfit as stale in the same
	// breath.
	if n.Outfit != "" {
		entry.OutfitName = n.Outfit
		entry.OutfitVer = h.outfitVer(vers, n.Version, n.Outfit)
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
// to dial: never an agent. This is what makes `figaro attend` free.
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
// must still be an error, attending a typo has to fail, not open a socket
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
	// legitimately have none: see backfill.go), and Open would decode the
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
	// address is a pure function of the id, so nothing here needs an agent -
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
//
// This is the seam that lets the bash tool honor a runtime
// `figaro set system.cwd …` without rebuilding the registry.
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
		SearchPaths: []string{loadedOutfitPath(h, name)},
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
