package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/internal/decide"
	"log/slog"
	"sync"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/gc"
	"github.com/jack-work/figaro/internal/methods"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkrpc"
)

type ProviderFactory = figaro.ProviderFactory

type ServerConfig struct {
	Angelus         *Angelus
	Config          *config.Loaded
	ProviderFactory ProviderFactory
	Ctx             context.Context

	// AvailableProviders is surfaced in typed errors for first-run selection.
	AvailableProviders []string

	FormTemplates *template.Template // nil = skip

	Decider *decide.Engine
}

type Handlers struct {
	Map map[string]jkrpc.HandlerFunc
	h   *handlers
}

// noticeUpgrade flags the default form when the binary's bundled skills have moved.
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

func NewHandlers(cfg ServerConfig) *Handlers {
	// Before any outfit is folded and before noticeUpgrade reads the bundled root.
	if cfg.Config != nil {
		outfit.SetBundledSkills(cfg.Config.BundledSkills())
	}
	h := &handlers{
		angelus:            cfg.Angelus,
		config:             cfg.Config,
		factory:            cfg.ProviderFactory,
		ctx:                inProcess(cfg.Ctx),
		formTmpls:          cfg.FormTemplates,
		outfitter:          newOutfitter(cfg.Angelus, cfg.Config),
		availableProviders: cfg.AvailableProviders,
		decider:            cfg.Decider,
		breaker:            methods.NewBreaker(),
	}
	if cfg.Config != nil {
		h.outfitter.Warm(cfg.Config.Config.DefaultOutfit)
	}
	h.noticeUpgrade()
	h.startReapedForm()
	h.startTroupeForm()
	h.publishCatalogs()
	methods := map[string]jkrpc.HandlerFunc{
		rpc.MethodCreate:             h.create,
		rpc.MethodFormCreate:         h.formCreate,
		rpc.MethodFormBind:           h.formBind,
		rpc.MethodOutfitReload:       h.outfitReload,
		rpc.MethodFork:               h.fork,
		rpc.MethodPromote:            h.promote,
		rpc.MethodDemote:             h.demote,
		rpc.MethodImport:             h.importAria,
		rpc.MethodOutfits:            h.outfits,
		rpc.MethodConfigure:          h.configure,
		rpc.MethodNormalize:          h.normalize,
		rpc.MethodGC:                 h.gc,
		rpc.MethodGCPlan:             h.gcPlan,
		rpc.MethodGCApply:            h.gcApply,
		rpc.MethodGCUndo:             h.gcUndo,
		rpc.MethodKill:               h.kill,
		rpc.MethodReapedRestore:      h.reapedRestore,
		rpc.MethodModelsPick:         h.modelsPick,
		rpc.MethodList:               h.list,
		rpc.MethodAttach:             h.attach,
		rpc.MethodBind:               h.bind,
		rpc.MethodResolve:            h.resolve,
		rpc.MethodUnbind:             h.unbind,
		rpc.MethodStatus:             h.status,
		rpc.MethodMemCollect:         h.memCollect,
		rpc.MethodPprofArm:           h.pprofArm,
		rpc.MethodQuarantineList:     h.quarantineList,
		rpc.MethodQuarantineRelease:  h.quarantineRelease,
		rpc.MethodProviderLedger:     h.providerLedger,
		rpc.MethodModelsWant:         h.modelsWant,
		rpc.MethodModelsRefresh:      h.modelsRefresh,
		rpc.MethodSaveBindings:       h.saveBindings,
		rpc.MethodHaltAndStop:        h.haltAndStop,
		rpc.MethodIR:                 h.ariaRead,
		rpc.MethodLineage:            h.lineage,
		rpc.MethodDecide:             h.decide,
		rpc.MethodHello:              h.hello,
		rpc.MethodNodeResolve:        h.nodeResolve,
		rpc.MethodTroupeMembers:      h.troupeMembers,
		rpc.MethodTroupeEntries:      h.troupeEntries,
		rpc.MethodTroupeSign:         h.troupeSign,
		rpc.MethodTroupeIdentity:     h.troupeIdentity,
		rpc.MethodTroupeLeave:        h.troupeLeave,
		rpc.MethodTroupePolicy:       h.troupePolicy,
		rpc.MethodTroupePolicySet:    h.troupePolicySet,
		rpc.MethodTroupeAPIKeyMint:   h.troupeAPIKeyMint,
		rpc.MethodTroupeAPIKeyRevoke: h.troupeAPIKeyRevoke,
		rpc.MethodTroupeAPIKeyList:   h.troupeAPIKeyList,
		rpc.MethodMove:               h.move,
		rpc.MethodImportBegin:        h.importBegin,
		rpc.MethodImportChunk:        h.importChunk,
		rpc.MethodImportCommit:       h.importCommit,
		rpc.MethodImportAbort:        h.importAbort,
		rpc.MethodImportStatus:       h.importStatus,
		rpc.MethodNodeLearn:          h.nodeLearn,
	}
	h.federate()
	h.mover()
	if h.holdDepartures() {
		h.ensureSettling()
	}
	return &Handlers{Map: h.door(methods, h.policy()), h: h}
}

func (h *handlers) door(daemon map[string]jkrpc.HandlerFunc, policy authz.Policy) map[string]jkrpc.HandlerFunc {
	methods := make(map[string]jkrpc.HandlerFunc, len(daemon)+len(figaro.AgentMethods()))
	for m, fn := range daemon {
		methods[m] = fn
	}
	for _, m := range figaro.AgentMethods() {
		methods[m] = h.ariaMethod(m)
	}
	methods[rpc.MethodFollow] = h.ariaMethod(rpc.MethodFollow)
	guarded := authz.Guard(methods, authz.AriaHeader{}, policy)
	if list, ok := guarded[rpc.MethodList]; ok {
		guarded[rpc.MethodList] = h.listingPeers(list)
	}
	for m, fn := range guarded {
		guarded[m] = h.resolvingIDs(m, h.touching(m, fn))
	}
	return guarded
}

func (h *handlers) ariaMethod(method string) jkrpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		id := rpc.AriaOf(params)
		if id == "" {
			return nil, fmt.Errorf("%s: no %s: an aria method names its aria", method, rpc.AriaKey)
		}
		if isCallerFormID(id) {
			return h.callerFormMethod(ctx, id, method, params)
		}
		hb := h.angelus.Hubs.get(id)
		if hb == nil {
			if err := h.requireAria(id); err != nil {
				return nil, fmt.Errorf("%s: %w", method, err)
			}
			hb = h.hubFor(id)
		}
		if f := followerOf(ctx); f != nil && rpc.MethodFollows(method) && h.allowed(ctx, rpc.VerbListen, authz.Aria(id)) == nil {
			done, err := h.angelus.guard.enter(id)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", method, err)
			}
			f.follow(hb)
			done()
		}
		if method == rpc.MethodFollow {
			return rpc.FollowResponse{OK: true, FigaroID: id}, nil
		}
		if err := h.refuseCallerPatch(ctx, method, id, params); err != nil {
			return nil, err
		}
		dressed, err := h.dressParams(ctx, method, params)
		if err != nil {
			return nil, err
		}
		if dressed, err = h.holdCastInBox(ctx, method, id, dressed); err != nil {
			return nil, err
		}
		if err := h.checkAriaMethod(ctx, method, id, dressed); err != nil {
			return nil, err
		}
		if err := h.refuseRetired(method, id, dressed); err != nil {
			return nil, err
		}
		if err := h.refuseCredoChange(method, id, dressed); err != nil {
			return nil, err
		}
		if targets := roleTargetsIn(method, dressed); len(targets) > 0 {
			done, err := h.angelus.guard.enter(targets...)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", method, err)
			}
			defer done()
		}
		return hb.route(ctx, method, dressed)
	}
}

func (h *handlers) hello(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.HelloRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
	}
	if req.Version != rpc.ProtocolVersion {
		return nil, fmt.Errorf("protocol version %d; this node speaks %d", req.Version, rpc.ProtocolVersion)
	}
	return rpc.HelloResponse{Version: rpc.ProtocolVersion, Capabilities: daemonCapabilities, Node: h.nodeID(), Principal: authz.PrincipalOf(ctx)}, nil
}

func (h *handlers) nodeID() string {
	if h.angelus.Backend == nil {
		return ""
	}
	return h.angelus.Backend.NodeID()
}

// Restore wakes ariaID; wokeBy lands on FigaroInfo.WokeBy.
func (hs *Handlers) Restore(ctx context.Context, ariaID, wokeBy string) (figaro.Figaro, error) {
	return hs.h.restoreByID(ctx, ariaID, wokeBy)
}

func (hs *Handlers) RemoveAria(id string, recursive bool) error {
	return hs.h.RemoveAria(id, recursive)
}

func (hs *Handlers) RemoveAriaIf(id string, expect []string) error {
	return hs.h.RemoveAriaIf(id, expect)
}

func (hs *Handlers) RequireAria(ariaID string) error {
	return hs.h.requireAria(ariaID)
}

type handlers struct {
	angelus            *Angelus
	config             *config.Loaded
	factory            ProviderFactory
	ctx                context.Context
	formTmpls          *template.Template
	outfitter          *outfit.Outfitter
	availableProviders []string
	decider            *decide.Engine
	breaker            *methods.Breaker
	fed                *federation
	policySign         sync.Mutex

	cappedMu sync.Mutex
	capped   map[string]time.Time

	reapedForm *store.Form
	troupeForm *store.Form
	reapedMu   sync.Mutex

	gcOnce      sync.Once
	gcCollector *gc.Collector
	gcReapHook  func(stage, id string)

	arias        ariasForms
	outfitHashes outfitHashes

	// readerInst's per-aria servers are the window; a reader per call would retain nothing.
	readerOnce sync.Once
	readerInst *AriaReader

	// configMu guards config and outfitter together against reload.
	configMu sync.Mutex

	// restoring is a single-flight, not a lock table: an entry exists only
	// while a wake is running.
	restoreMu sync.Mutex
	restoring map[string]*restoreCall
	releasing map[string]bool

	moving    moving
	settling  atomic.Bool
	grafts    grafts
	moverOnce sync.Once
	mv        mover
	moves     *store.Moves
}

// settings returns config and outfitter under one lock so they are never seen half-swapped.
func (h *handlers) settings() (*config.Loaded, *outfit.Outfitter) {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	return h.config, h.outfitter
}

// openAriaForm seeds a hot view from the form channel; nil on failure.
func (h *handlers) openAriaForm(ariaID string) *form.State {
	if h.formTmpls == nil {
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

// currentOutfitHash is re-resolved from disk; "" when it cannot be.
func (h *handlers) currentOutfitHash(name string) (current, legacy string) {
	_, ofit := h.settings()
	if ofit == nil {
		return "", ""
	}
	gen := ofit.Generation()
	if hashes, ok := h.outfitHashes.get(ofit, gen, name); ok {
		return hashes[0], hashes[1]
	}
	names, perr := outfit.TermNames(name)
	if perr != nil || len(names) == 0 {
		return "", ""
	}
	p, err := ofit.Names(names...)
	if err != nil {
		return "", ""
	}
	current, _ = store.OutfitVersion(name, p)
	legacy, _ = store.LegacyOutfitVersion(p)
	h.outfitHashes.put(ofit, gen, name, [2]string{current, legacy})
	return current, legacy
}

type outfitHashes struct {
	mu     sync.Mutex
	of     *outfit.Outfitter
	gen    uint64
	byName map[string][2]string
}

func (c *outfitHashes) get(of *outfit.Outfitter, gen uint64, name string) ([2]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.of != of || c.gen != gen {
		return [2]string{}, false
	}
	h, ok := c.byName[name]
	return h, ok
}

func (c *outfitHashes) put(of *outfit.Outfitter, gen uint64, name string, h [2]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.of != of || c.gen != gen {
		c.of, c.gen, c.byName = of, gen, map[string][2]string{}
	}
	c.byName[name] = h
}

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

// formCreate folds no default outfit; the patch is the form. The hub is
// stood up before the response: unix sockets have no lazy activation.
func (h *handlers) formCreate(ctx context.Context, params json.RawMessage) (interface{}, error) {
	_, span := figOtel.Start(ctx, "angelus.formCreate")
	defer span.End()

	var req rpc.FormCreateRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := h.refuseCallerKeys(ctx, form.Snapshot{}, req.Patch); err != nil {
		return nil, err
	}
	if req.Patch != nil {
		if targets := roleTargetsOf(*req.Patch); len(targets) > 0 {
			done, err := h.angelus.guard.enter(targets...)
			if err != nil {
				return nil, fmt.Errorf("form.create: %w", err)
			}
			defer done()
		}
	}
	if req.Parent != "" {
		parent, err := h.canonicalID(ctx, req.Parent)
		if err != nil {
			return nil, fmt.Errorf("form.create: %w", err)
		}
		req.Parent = parent
		if err := h.allowed(ctx, rpc.VerbRead, authz.Aria(req.Parent)); err != nil {
			return nil, fmt.Errorf("form.create: a copy of %s reads it: %w", req.Parent, err)
		}
	}
	if err := h.refuseBoxOutfits(ctx, req.Outfits, false); err != nil {
		return nil, err
	}
	patch, err := h.dress(req.Outfits, req.Patch)
	if err != nil {
		return nil, err
	}
	if patch.IsIdentity() {
		return nil, fmt.Errorf("form.create: a form is born of its patch and outfits; an empty one names nothing")
	}
	if patch, err = h.holdInCallerBox(ctx, patch); err != nil {
		return nil, fmt.Errorf("form.create: %w", err)
	}
	born := form.Snapshot{}
	if req.Parent != "" {
		if born, err = h.angelus.Backend.FormState(req.Parent); err != nil {
			return nil, err
		}
	}
	if err := h.refuseRoleOutOfBox(born.Apply(patch)); err != nil {
		return nil, fmt.Errorf("form.create: %w", err)
	}
	id, version, err := h.angelus.Backend.CreateForm(req.Parent, patch, mintedBy(ctx))
	if err != nil {
		return nil, err
	}
	return rpc.FormCreateResponse{FormID: id, Version: version}, nil
}

// formBind births a figaro dormant: no provider resolved, no agent, no
// registry entry, so `bind null` mints a naked figaro whose missing
// provider fails at the first turn. It never touches the caller's attendance.
func (h *handlers) formBind(ctx context.Context, params json.RawMessage) (interface{}, error) {
	_, span := figOtel.Start(ctx, "angelus.formBind")
	defer span.End()

	var req rpc.FormBindRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	req.Cwd = hostCwdOf(ctx, req.Cwd)
	if err := h.refuseCallerKeys(ctx, form.Snapshot{}, req.Patch); err != nil {
		return nil, err
	}
	parent := req.Parent
	if parent == "null" {
		parent = ""
	}
	if parent != "" {
		var err error
		if parent, err = h.canonicalID(ctx, parent); err != nil {
			return nil, fmt.Errorf("form.bind: %w", err)
		}
		if err := h.allowed(ctx, rpc.VerbRead, authz.Aria(parent)); err != nil {
			return nil, fmt.Errorf("form.bind: a fork of %s reads it: %w", parent, err)
		}
		n, ok := h.angelus.Backend.Node(parent)
		isStump := ok && n.Kind == "outfit"
		if !ok || (n.Kind != store.KindForm && !isStump) {
			return nil, fmt.Errorf("form.bind: %s is not an unbound form (bind forks forms; a figaro forks with `fig fork`)", req.Parent)
		}
	}
	if err := h.refuseBoxOutfits(ctx, req.Outfits, true); err != nil {
		return nil, err
	}
	dress, err := h.dress(req.Outfits, req.Patch)
	if err != nil {
		return nil, err
	}
	cwd := birthCwd(req.Cwd)
	chosen := isDir(req.Cwd)
	id, _, err := h.angelus.Backend.ForkWith(parent, 0, childBirthPatch(dress, cwd), mintedBy(ctx))
	if err != nil {
		return nil, fmt.Errorf("form.bind: mint figaro: %w", err)
	}
	if _, err := h.angelus.Backend.ApplyFormPrivileged(id, convBootPatch(id, cwd)); err != nil {
		h.discardBirth(id)
		return nil, fmt.Errorf("form.bind: stamp aria id: %w", err)
	}
	if err := h.grantSandboxOn(ctx, id, chosen || patchTouches(dress, "system.cwd")); err != nil {
		h.discardBirth(id)
		return nil, fmt.Errorf("form.bind: %w", err)
	}
	if err := h.placeWorkspace(id, chosen, patchTouches(dress, "system.cwd")); err != nil {
		h.discardBirth(id)
		return nil, fmt.Errorf("form.bind: %w", err)
	}
	return rpc.FormBindResponse{FigaroID: id}, nil
}

const (
	nullKind         = "null"
	outfitKind       = "outfit"
	conversationKind = "conversation"
)

const ariaReadHardCap = 1000

func inProcess(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return authz.WithDoor(ctx, authz.DoorInProcess)
}
