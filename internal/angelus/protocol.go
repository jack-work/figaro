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

// NewHandlers creates the handler set for the angelus socket.
func NewHandlers(cfg ServerConfig) *Handlers {
	// Before ANY outfit is folded, and before noticeUpgrade reads the bundled
	// root: the switch has to be thrown ahead of the first resolution, or the
	// daemon composes one form with the bundled skills and the next without.
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
	// Warm the ONE closure `fig new` is certain to want, in the background.
	// Nothing blocks on it: startup reads no outfit file, and every other name
	// is folded when someone asks for it.
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
// RemoveAria deletes a node and everything a deletion owes: see the handler
// of the same name. It is what the TTL sweep calls.
func (hs *Handlers) RemoveAria(id string, recursive bool) error {
	return hs.h.RemoveAria(id, recursive)
}

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

	// readerOnce/readerInst memoize the windowed reader: its per-aria
	// servers ARE the window, and a reader minted per call would retain
	// nothing and recompose everything, which is the old behaviour with
	// extra steps.
	readerOnce sync.Once
	readerInst *AriaReader

	// configMu guards config against concurrent reload + read. The
	// reload-from-disk is cheap, but other handlers may dereference
	// h.config concurrently.
	configMu sync.Mutex

	// restoring is the in-flight wake per aria. It is a single-flight, not a
	// lock table: the entry EXISTS only while a wake is running, so nothing
	// accumulates. The map it replaces handed out a *sync.Mutex per aria and
	// never removed one, so it grew by one entry per aria ever restored, for
	// the life of the daemon (plans/lock-audit.md, fast-follow 2).
	restoreMu sync.Mutex
	restoring map[string]*restoreCall
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

// currentOutfitHash is the content hash the outfit would have right now
// (recomputed from the on-disk definition), or "" if it can't be loaded.
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

// outfitKind / nullKind / conversationKind mirror the store's nodeKind string
// values (the store package's constants are unexported).
const (
	nullKind         = "null"
	outfitKind       = "outfit"
	conversationKind = "conversation"
)

// ariaReadHardCap bounds Limit on aria.read regardless of what the
// client asks for, so a misconfigured client can't pull megabytes of
// IR in a single RPC.
const ariaReadHardCap = 1000
