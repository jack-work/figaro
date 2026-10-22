package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/sandbox"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/turns"
	"github.com/jack-work/figaro/internal/uiir"
)

func (h *handlers) create(ctx context.Context, params json.RawMessage) (interface{}, error) {
	_, span := figOtel.Start(ctx, "angelus.create")
	defer span.End()

	var req rpc.CreateRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}

	loaded, _ := h.settings()

	outfitName := loaded.Config.DefaultOutfit
	named := len(req.Outfits) > 0
	if named {
		outfitName = strings.Join(req.Outfits, ",")
	}

	var stumpPatch form.Patch
	var err error
	if named {
		stumpPatch, err = h.dress(req.Outfits, nil)
	} else {
		stumpPatch, err = h.dressDefault()
	}
	if err != nil {
		return nil, err
	}

	// The child carries only the caller's keys and the runtime fill-ins; the
	// outfit names are in the parent.
	dress, err := h.dress(nil, req.Patch)
	if err != nil {
		return nil, err
	}
	base := mergePatches(stumpPatch, dress)
	// A named default missing on disk falls through to the missing-provider error.
	if base.IsIdentity() && outfitName == "" {
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

	cwd := birthCwd(req.Cwd)
	chosen := isDir(req.Cwd)

	backend := h.angelus.Backend

	cbState, _ := form.Open("")
	var id string

	{
		// Forking the shared default form is what shares one rendered prefix
		// and one warm provider cache across every aria on the same outfit.
		formID, serr := h.birthParent(backend, stumpPatch, outfitName, named)
		if serr != nil {
			return nil, serr
		}
		var cerr error
		id, _, cerr = backend.ForkWith(formID, 0, childBirthPatch(dress, cwd), mintedBy(ctx))
		if cerr != nil {
			return nil, fmt.Errorf("mint aria: %w", cerr)
		}
	}
	born := false
	defer func() {
		if !born {
			h.discardBirth(id)
		}
	}()
	{
		// aria_id rides a second patch so the birth patch stays a pure
		// function of what was asked for.
		if _, aerr := backend.ApplyFormPrivileged(id, convBootPatch(id, cwd)); aerr != nil {
			return nil, fmt.Errorf("stamp aria id: %w", aerr)
		}
		if err := h.grantSandboxOn(ctx, id, chosen || patchTouches(dress, "system.cwd")); err != nil {
			return nil, err
		}
		if err := h.placeWorkspace(id, chosen); err != nil {
			return nil, err
		}
		snap, serr := backend.FormState(id)
		if serr != nil {
			return nil, fmt.Errorf("read conversation form: %w", serr)
		}
		cbState.Apply(snap.AsPatch())
	}

	formStr := func(key string) string {
		if s := cbState.Snapshot().Lookup(key); s != nil {
			return *s
		}
		return ""
	}
	box, err := sandbox.Open(ctx, h.angelus.Sessions, formStr("system.sandbox"), id, formStr("system.cwd"), h.angelus.RuntimeDir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	born = true
	reg := tool.DefaultRegistry(box, cwdFromForm(cbState, box),
		tool.WithImageBudget(loaded.InlineImageBudget()))
	agent := figaro.NewAgent(figaro.Config{
		ID:              id,
		Provider:        prov,
		ProviderFactory: h.factory,
		Models:          h.angelus.Models,
		Tools:           reg,
		Projector:       uiir.New(reg).Naming(h.angelus.senderName),
		Backend:         backend,
		Form:            cbState,
		Settings:        loaded,
		UICache:         h.angelus.UICache,
		Decider:         h.methodAsker(),
		DefaultForm:     h.defaultForm,
	})

	if err := h.angelus.Registry.Register(agent); err != nil {
		agent.Kill()
		return nil, err
	}

	agent.OnTeardown(h.bindAgentToHub(id, agent))

	slog.Info("created figaro",
		"id", id, "outfit", outfitName, "provider", provName, "model", knobs.Model)

	return rpc.CreateResponse{FigaroID: id}, nil
}

func (h *handlers) checkForkLT(ariaID string, lt uint64) error {
	if lt == 0 {
		return nil
	}
	log, err := h.angelus.Backend.OpenFigIR(ariaID)
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

// forkPointOf maps a turn to the main-LT a fork takes.
func (h *handlers) forkPointOf(ariaID string, turn uint64) (uint64, error) {
	if turn == 0 {
		return 0, nil
	}
	log, err := h.angelus.Backend.OpenFigIR(ariaID)
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

// fork never kills or restarts the addressed trunk: it keeps its id and
// stays live; the alternative is a new dormant conversation.
func (h *handlers) fork(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ForkRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	var cont, alt string
	note := ""
	var forkOwner store.OwnerInfo
	// A turn and an LT both name a fork point; neither is inferable from the other.
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
	dress, err := h.dress(req.Outfits, req.Patch)
	if err != nil {
		return nil, err
	}
	// interior is read off the coordinate, not the LT: forking at turn 1 has
	// LT 0, which is also the head-fork sentinel.
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
		// The branch and its dressing are one call: a patch sent afterwards
		// can be acked on the parent and miss the child.
		at := uint64(0)
		if interior {
			at = atMainLT
		}
		var ferr error
		alt, _, ferr = h.angelus.Backend.ForkWith(req.FigaroID, at, forkDress(dress, req.FigaroID), mintedBy(ctx))
		if ferr != nil {
			return ferr
		}
		if gerr := h.grantSandboxOn(ctx, alt, patchTouches(dress, "system.cwd")); gerr != nil {
			h.discardBirth(alt)
			alt = ""
			return gerr
		}
		cont = req.FigaroID
		h.seedForkMeta(parentMeta, req.FigaroID, alt, atMainLT, interior, forkOwner)
		// The child inherited its parent's aria_id; re-stamp it.
		if _, aerr := h.angelus.Backend.ApplyForm(alt, withAriaID(form.Patch{}, alt)); aerr != nil {
			slog.Warn("fork: stamp child aria id", "alt", alt, "err", aerr)
		}
		return nil
	}

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
		copy.ContextExact = info.ContextExact
		copy.CreatedAtMS = info.CreatedAt.UnixMilli()
		copy.LastFigaroLT = info.LastFigaroLT
	}
	return &copy
}

func (h *handlers) seedForkMeta(meta *store.AriaMeta, parent, child string, atMainLT uint64, interior bool, owner store.OwnerInfo) {
	if meta == nil {
		return
	}
	copy := *meta
	copy.CreatedAtMS = time.Now().UnixMilli()
	copy.LastAccessMS = 0
	own, _ := h.angelus.Backend.Meta(child)
	if own == nil {
		own = &store.AriaMeta{}
	}
	if own.CreatedAtMS != 0 {
		copy.CreatedAtMS = own.CreatedAtMS
	}
	copy.TTLMS, copy.KeepaliveMS, copy.RetiredAtMS = own.TTLMS, own.KeepaliveMS, own.RetiredAtMS
	copy.ReapedAtMS, copy.ReapedRoot = own.ReapedAtMS, own.ReapedRoot
	if interior {
		copy.MessageCount = h.messageCountAt(parent, atMainLT)
		copy.TurnCount = 0
		copy.TokensIn = 0
		copy.TokensOut = 0
		copy.CacheReadTokens = 0
		copy.CacheWriteTokens = 0
		copy.ContextTokens = 0
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
		count--
	}
	for count > 0 {
		node, ok := h.angelus.Backend.Node(id)
		if !ok || node.Parent == "" {
			break
		}
		if parent, ok := h.angelus.Backend.Node(node.Parent); ok && parent.Kind == string(outfitKind) {
			count--
			break
		}
		id = node.Parent
	}
	return max(0, count)
}

func (h *handlers) gc(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.GCRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
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

func (h *handlers) importAria(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ImportRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if req.Outfit == "" {
		return nil, errors.New("import: no outfit named")
	}
	outfitID, err := h.angelus.Backend.CreateOutfit(req.Outfit, req.OutfitPatch)
	if err != nil {
		return nil, fmt.Errorf("import: outfit %q: %w", req.Outfit, err)
	}
	id, err := h.angelus.Backend.CreateConversation(outfitID, mintedBy(ctx))
	if err != nil {
		return nil, fmt.Errorf("import: create conversation: %w", err)
	}
	log, err := h.angelus.Backend.OpenFigIR(id)
	if err != nil {
		return nil, fmt.Errorf("import: open %q: %w", id, err)
	}
	for i, m := range req.Messages {
		if _, err := log.Append(store.Entry[message.Message]{Payload: m}); err != nil {
			return nil, fmt.Errorf("import: append message %d of %d: %w", i+1, len(req.Messages), err)
		}
	}
	// The form last, as one patch; aria_id is re-stamped because the export
	// carries the id it had in the source store.
	keys := map[string]json.RawMessage{}
	for _, e := range req.Form.Entries() {
		if !e.IsRemoval() {
			keys[e.Key] = e.New
		}
	}
	// system.studies is refcounted on a shared libretto, so it is replayed
	// through the verb below rather than copied as a key.
	var studies []string
	if raw, ok := keys[figaro.StudiesKey]; ok {
		if err := json.Unmarshal(raw, &studies); err != nil {
			return nil, fmt.Errorf("import: %s: %w", figaro.StudiesKey, err)
		}
		delete(keys, figaro.StudiesKey)
	}
	if b, mErr := json.Marshal(id); mErr == nil {
		keys["aria_id"] = b
	}
	if _, err := h.angelus.Backend.ApplyForm(id, form.Build(form.Snapshot{}, keys, nil)); err != nil {
		return nil, fmt.Errorf("import: form: %w", err)
	}
	_, cwdSet := keys["system.cwd"]
	if err := h.grantSandboxOn(ctx, id, cwdSet); err != nil {
		h.discardBirth(id)
		return nil, fmt.Errorf("import: %w", err)
	}
	// A study naming a form this store lacks is not an error: the libretto
	// holds an empty copy and follows if that form ever arrives.
	for _, formID := range studies {
		if _, err := studyThroughStore(h.angelus.Backend, id, formID, false); err != nil {
			slog.Warn("import: could not restore a study",
				"aria", id, "form", formID, "err", err)
		}
	}
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

func (h *handlers) kill(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.KillRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if req.Recursive {
		for _, d := range h.descendants(req.FigaroID) {
			if err := h.allowed(ctx, rpc.VerbKill, authz.Aria(d)); err != nil {
				return nil, fmt.Errorf("kill -r %s: %w", req.FigaroID, err)
			}
		}
	}
	if err := h.RemoveAria(req.FigaroID, req.Recursive); err != nil {
		return nil, err
	}
	return rpc.KillResponse{OK: true}, nil
}

// RemoveAria is the whole of a deletion: agent, background sessions, bytes, endpoint.
func (h *handlers) RemoveAria(id string, recursive bool) error {
	if h.angelus.Registry.Get(id) != nil {
		if err := h.angelus.Registry.Kill(id); err != nil {
			return err
		}
	}

	// Unconditional: a hibernated aria has no live agent and still owns running children.
	if h.angelus.Sessions != nil {
		if n := h.angelus.Sessions.KillScope(id); n > 0 {
			slog.Info("killed aria sessions", "id", id, "sessions", n)
		}
	}

	var held []string
	if h.angelus.Backend != nil {
		removed := []string{id}
		if recursive {
			removed = append(removed, h.subtree(id)...)
		}
		held = h.ownWorkspaces(removed)
		if err := h.angelus.Backend.Remove(id, recursive); err != nil {
			return err
		}
	}
	h.releaseWorkspaces(held)

	// The endpoint outlives the agent, not the aria; this EOF must not happen on hibernate.
	if hb := h.angelus.Hubs.drop(id); hb != nil {
		hb.Close()
	}

	slog.Info("killed figaro", "id", id)
	return nil
}

func mintedBy(ctx context.Context) store.BirthOption {
	return store.MintedBy(authz.PrincipalText(authz.PrincipalOf(ctx)))
}
