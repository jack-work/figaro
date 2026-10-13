package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"text/template"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
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
		ctx:                cfg.Ctx,
		formTmpls:          cfg.FormTemplates,
		outfitter:          newOutfitter(cfg.Angelus, cfg.Config),
		availableProviders: cfg.AvailableProviders,
	}
	if cfg.Config != nil {
		h.outfitter.Warm(cfg.Config.Config.DefaultOutfit)
	}
	h.noticeUpgrade()
	return &Handlers{
		Map: authz.Guard(map[string]jkrpc.HandlerFunc{
			rpc.MethodCreate:         h.create,
			rpc.MethodFormCreate:     h.formCreate,
			rpc.MethodFormBind:       h.formBind,
			rpc.MethodOutfitReload:   h.outfitReload,
			rpc.MethodFork:           h.fork,
			rpc.MethodPromote:        h.promote,
			rpc.MethodImport:         h.importAria,
			rpc.MethodOutfits:        h.outfits,
			rpc.MethodConfigure:      h.configure,
			rpc.MethodNormalize:      h.normalize,
			rpc.MethodGC:             h.gc,
			rpc.MethodKill:           h.kill,
			rpc.MethodList:           h.list,
			rpc.MethodAttach:         h.attach,
			rpc.MethodBind:           h.bind,
			rpc.MethodResolve:        h.resolve,
			rpc.MethodUnbind:         h.unbind,
			rpc.MethodStatus:         h.status,
			rpc.MethodMemCollect:     h.memCollect,
			rpc.MethodProviderLedger: h.providerLedger,
			rpc.MethodSaveBindings:   h.saveBindings,
			rpc.MethodIR:             h.ariaRead,
			rpc.MethodRead:           h.read,
			rpc.MethodContext:        h.context,
			rpc.MethodForm:           h.form,
			rpc.MethodLineage:        h.lineage,
			rpc.MethodHello:          h.hello,
		}, authz.AriaHeader{}, h.policy()),
		h: h,
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
	return rpc.HelloResponse{Version: rpc.ProtocolVersion, Principal: authz.PrincipalOf(ctx)}, nil
}

func (h *handlers) policy() authz.Policy {
	if h.config != nil && h.config.AuthzPolicy() == "default" {
		return authz.DefaultRules(h.turnActive)
	}
	return authz.AllowAll()
}

func (h *handlers) turnActive(ariaID string) bool {
	if h.angelus == nil || h.angelus.Registry == nil {
		return false
	}
	live := h.angelus.Registry.Get(ariaID)
	return live != nil && live.TurnActive()
}

// Restore wakes ariaID; wokeBy lands on FigaroInfo.WokeBy.
func (hs *Handlers) Restore(ctx context.Context, ariaID, wokeBy string) (figaro.Figaro, error) {
	return hs.h.restoreByID(ctx, ariaID, wokeBy)
}

func (hs *Handlers) RemoveAria(id string, recursive bool) error {
	return hs.h.RemoveAria(id, recursive)
}

// OpenEndpoint makes an aria dialable without waking it.
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

	// readerInst's per-aria servers are the window; a reader per call would retain nothing.
	readerOnce sync.Once
	readerInst *AriaReader

	// configMu guards config and outfitter together against reload.
	configMu sync.Mutex

	// restoring is a single-flight, not a lock table: an entry exists only
	// while a wake is running.
	restoreMu sync.Mutex
	restoring map[string]*restoreCall
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
	return current, legacy
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
	if req.Patch == nil || req.Patch.IsIdentity() {
		return nil, fmt.Errorf("form.create: a form is born of its patch; an empty one names nothing")
	}
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
	cwd := birthCwd(req.Cwd)
	id, _, err := h.angelus.Backend.ForkWith(parent, 0, childBirthPatch(dress, cwd))
	if err != nil {
		return nil, fmt.Errorf("form.bind: mint figaro: %w", err)
	}
	if _, err := h.angelus.Backend.ApplyFormPrivileged(id, convBootPatch(id, cwd)); err != nil {
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

const (
	nullKind         = "null"
	outfitKind       = "outfit"
	conversationKind = "conversation"
)

const ariaReadHardCap = 1000
