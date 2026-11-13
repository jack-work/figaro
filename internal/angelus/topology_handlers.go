package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/gc"
	"github.com/jack-work/figaro/internal/hierarchy"
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
	req.Cwd = hostCwdOf(ctx, req.Cwd)
	if err := h.refuseCallerKeys(ctx, lineage{}, form.Snapshot{}, req.Patch); err != nil {
		return nil, err
	}

	loaded, _ := h.settings()

	if err := h.refuseBoxOutfits(ctx, req.Outfits, true); err != nil {
		return nil, err
	}
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
		if err := h.grantSandboxOn(ctx, id); err != nil {
			return nil, err
		}
		if err := h.dropHostCwdInBox(id, patchTouches(dress, "system.cwd")); err != nil {
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
	if err := h.providerAllowedIn(formStr("system.sandbox"), provName); err != nil {
		return nil, err
	}
	box, err := sandbox.Open(ctx, h.angelus.Sessions, loaded, formStr("system.sandbox"), id, formStr("system.cwd"))
	if err != nil {
		return nil, fmt.Errorf("sandbox: %w", err)
	}
	born = true
	reg := tool.DefaultRegistry(box, cwdFromForm(cbState, box),
		tool.WithImageBudget(loaded.InlineImageBudget()))
	agent := figaro.NewAgent(figaro.Config{
		ID:              id,
		Provider:        prov,
		ProviderFactory: h.factoryIn(formStr("system.sandbox")),
		Models:          h.angelus.Models,
		Tools:           reg,
		Projector:       uiir.New(reg).Naming(h.angelus.senderName),
		Backend:         backend,
		Form:            cbState,
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
		return nil, err
	}

	agent.OnTeardown(h.bindAgentToHub(id, agent))
	agent.OnTeardown(func() { box.Close() })
	agent.Redeliver()

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
func (h *handlers) forkPointOf(ariaID string, turn uint64) (lt uint64, head bool, err error) {
	if turn == 0 {
		return 0, true, nil
	}
	log, err := h.angelus.Backend.OpenFigIR(ariaID)
	if err != nil {
		return 0, false, fmt.Errorf("fork: open %s: %w", ariaID, err)
	}
	entries := log.Read()
	msgs := make([]message.Message, len(entries))
	for i, e := range entries {
		msgs[i] = e.Payload
		msgs[i].LogicalTime = e.LT
	}
	first, _, ok := turns.Span(msgs, turn)
	if ok {
		return first - 1, false, nil
	}
	last := turns.StampIDs(msgs)
	if turn == last+1 {
		return 0, true, nil
	}
	if last > 0 {
		return 0, false, fmt.Errorf("aria %s has no turn %d (it has turns 1..%d; %d is its head)", ariaID, turn, last, last+1)
	}
	return 0, false, fmt.Errorf("aria %s has no turns yet", ariaID)
}

// fork never kills or restarts the addressed trunk: it keeps its id and
// stays live; the alternative is a new dormant conversation.
func (h *handlers) fork(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ForkRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	inherited, _ := h.angelus.Backend.FormState(req.FigaroID)
	if err := h.refuseCallerKeys(ctx, birthUnder(req.FigaroID), inherited, req.Patch); err != nil {
		return nil, err
	}
	if err := h.refuseUnheldSandbox(ctx, req.Patch); err != nil {
		return nil, fmt.Errorf("fork: %w", err)
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
		lt, head, terr := h.forkPointOf(req.FigaroID, req.AtTurn)
		if terr != nil {
			return nil, terr
		}
		if head {
			req.AtTurn = 0
		}
		atMainLT = lt
	}
	if err := h.checkForkLT(req.FigaroID, req.AtLT); err != nil {
		return nil, err
	}
	if err := h.refuseBoxOutfits(ctx, req.Outfits, !h.headForkOfAConversation(req)); err != nil {
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
				note = fmt.Sprintf("%s lives in trunk %s: branching there, placed under %s", where, owner.Trunk, req.FigaroID)
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
		if interior && forkOwner.Trunk != "" && forkOwner.Trunk != req.FigaroID {
			if merr := h.place(ctx, alt, req.FigaroID); merr != nil {
				slog.Warn("fork: stays under the owner of its cut", "alt", alt, "under", forkOwner.Trunk, "err", merr)
			}
		}
		if gerr := h.grantSandboxOn(ctx, alt); gerr != nil {
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

func (h *handlers) headForkOfAConversation(req rpc.ForkRequest) bool {
	if req.AtTurn > 0 || req.AtLT > 0 {
		return false
	}
	n, ok := h.angelus.Backend.Node(req.FigaroID)
	return ok && n.Kind == store.KindConversation
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
	copy.ReapedAtMS, copy.ReapedRoot, copy.ReapedUntilMS = own.ReapedAtMS, own.ReapedRoot, own.ReapedUntilMS
	copy.ReapIfEmpty, copy.RetiredReason = own.ReapIfEmpty, own.RetiredReason
	if interior {
		held := h.heldBy(child)
		copy.MessageCount = held.MessageCount
		copy.TurnCount = held.TurnCount
		copy.TokensIn = 0
		copy.TokensOut = 0
		copy.CacheReadTokens = 0
		copy.CacheWriteTokens = 0
		copy.ContextTokens, copy.ContextExact = held.ContextTokens, held.ContextExact
		copy.LastFigaroLT = atMainLT
		copy.Provider, copy.Model, copy.Mantra, copy.Cwd = h.dressedAs(child)
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

func (h *handlers) dressedAs(id string) (provider, model, mantra, cwd string) {
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return "", "", "", ""
	}
	get := func(key string) string {
		if v := snap.Lookup(key); v != nil {
			return *v
		}
		return ""
	}
	return get("system.provider"), get("system.model"), get("mantra"), get("system.cwd")
}

func (h *handlers) heldBy(id string) store.AriaMeta {
	var held store.AriaMeta
	log, err := h.angelus.Backend.OpenFigIR(id)
	if err != nil {
		return held
	}
	for _, e := range log.Read() {
		held.Fold(e)
	}
	return held
}

func (h *handlers) gc(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.GCRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
	}
	caller := gc.Caller{Principal: authz.PrincipalText(authz.PrincipalOf(ctx))}
	c := h.collector()
	p, err := c.Plan(gc.Selector{}, caller)
	if err != nil {
		return nil, err
	}
	if p, err = c.Take(p.Run, caller.Principal); err != nil {
		return nil, err
	}
	failed := map[string]string{}
	if !req.DryRun {
		res, err := c.Apply(p)
		if err != nil {
			return nil, err
		}
		for _, f := range append(res.Failed, res.Dropped...) {
			failed[f.ID] = f.Why
		}
		if len(res.Collected) > 0 {
			slog.Info("collected outfit versions", "count", len(res.Collected))
		}
	}
	resp := rpc.GCResponse{DryRun: req.DryRun}
	for _, s := range p.Stumps {
		entry := rpc.GCStump{ID: s.ID, Outfit: s.Outfit, Version: s.Version, Children: s.Children}
		if s.Children == 0 {
			entry.Collected = true
			if why, bad := failed[s.ID]; bad {
				entry.Collected, entry.Err = false, why
			}
		}
		if entry.Collected {
			resp.Collected++
		}
		resp.Stumps = append(resp.Stumps, entry)
	}
	pass := h.angelus.retainForms(ctx, req.DryRun)
	resp.Retention, resp.FormsPlanned = string(pass.Mode), pass.DryRun
	if pass.Err != nil {
		resp.FormsErr = pass.Err.Error()
	}
	for _, f := range pass.Forms {
		resp.Forms = append(resp.Forms, rpc.GCForm{ID: f.ID, Pins: f.Pins, Segments: f.Dropped, Bytes: f.Bytes})
	}
	return resp, nil
}

func (h *handlers) normalize(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.NormalizeRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
	}
	report, err := h.angelus.Backend.Normalize(req.DryRun)
	if errors.Is(err, store.ErrNoTrunkCapability) {
		return rpc.NormalizeResponse{Unsupported: true}, nil
	}
	if err != nil {
		return nil, err
	}
	resp := rpc.NormalizeResponse{DryRun: req.DryRun, Detached: []rpc.NormalizedAria{}, Dropped: []rpc.DroppedOverride{}}
	named := func(id string) rpc.NormalizedAria {
		_, words := names(h.angelus.Backend, id)
		return rpc.NormalizedAria{ID: id, Handle: words}
	}
	for _, id := range report.Detached {
		resp.Detached = append(resp.Detached, named(id))
	}
	for _, st := range report.Dropped {
		_, under := names(h.angelus.Backend, st.Under)
		resp.Dropped = append(resp.Dropped, rpc.DroppedOverride{NormalizedAria: named(st.ID), Under: st.Under, UnderHandle: under, Reason: st.Reason})
	}
	if !req.DryRun {
		slog.Info("normalized", "detached", len(resp.Detached), "dropped", len(resp.Dropped))
	}
	return resp, nil
}

func importedKeys(p form.Patch) map[string]json.RawMessage {
	keys := map[string]json.RawMessage{}
	for _, e := range p.Entries() {
		if !e.IsRemoval() {
			keys[e.Key] = e.New
		}
	}
	if raw, ok := keys[form.RetiredGrantsKey]; ok {
		if _, both := keys[form.ScopesKey]; !both {
			keys[form.ScopesKey] = raw
			delete(keys, form.RetiredGrantsKey)
		}
	}
	return keys
}

func (h *handlers) importAria(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ImportRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if box, ok := callerBox(ctx); ok {
		return nil, fmt.Errorf("import: box %s may not import an aria: an export carries any form key", box)
	}
	if req.Outfit == "" {
		return nil, errors.New("import: no outfit named")
	}
	if !h.callerOwns(ctx) {
		if err := h.refuseCallerKeys(ctx, lineage{}, form.Snapshot{}, &req.OutfitPatch); err != nil {
			return nil, fmt.Errorf("import: outfit %q: %w", req.Outfit, err)
		}
		imported := form.Build(form.Snapshot{}, importedKeys(req.Form), nil)
		if err := h.refuseCallerKeys(ctx, lineage{}, form.Snapshot{}, &imported); err != nil {
			return nil, fmt.Errorf("import: %w", err)
		}
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
	keys := importedKeys(req.Form)
	// system.studies is refcounted on a shared libretto, so it is replayed
	// through the verb below rather than copied as a key.
	var studies []string
	if raw, ok := keys[figaro.StudiesKey]; ok {
		if err := json.Unmarshal(raw, &studies); err != nil {
			return nil, fmt.Errorf("import: %s: %w", figaro.StudiesKey, err)
		}
		delete(keys, figaro.StudiesKey)
	}
	studyKeys := map[string][]string{}
	for k, raw := range keys {
		fid, ok := strings.CutPrefix(k, store.StudyKeysKey+".")
		if !ok {
			continue
		}
		var held []string
		if json.Unmarshal(raw, &held) == nil {
			studyKeys[fid] = held
		}
		delete(keys, k)
	}
	if b, mErr := json.Marshal(id); mErr == nil {
		keys["aria_id"] = b
	}
	if _, err := h.angelus.Backend.ApplyForm(id, form.Build(form.Snapshot{}, keys, nil)); err != nil {
		return nil, fmt.Errorf("import: form: %w", err)
	}
	if err := h.grantSandboxOn(ctx, id); err != nil {
		h.discardBirth(id)
		return nil, fmt.Errorf("import: %w", err)
	}
	// A study naming a form this store lacks is not an error: the libretto
	// holds an empty copy and follows if that form ever arrives.
	for _, formID := range studies {
		if _, err := studyThroughStore(h.angelus.Backend, id, formID, studyKeys[formID], false); err != nil {
			slog.Warn("import: could not restore a study",
				"aria", id, "form", formID, "err", err)
		}
	}
	meta := &store.AriaMeta{
		OutfitName: req.Outfit,
		Mantra:     req.Mantra,
		Provider:   req.Provider,
		Model:      req.Model,
	}
	for _, e := range log.Read() {
		meta.Fold(e)
	}
	if err := h.angelus.Backend.SetMeta(id, meta); err != nil {
		slog.Warn("import: set meta", "id", id, "err", err)
	}
	slog.Info("imported aria", "id", id, "outfit", req.Outfit, "messages", len(req.Messages))
	return rpc.ImportResponse{
		FigaroID: id, Outfit: req.Outfit, Messages: len(req.Messages), WasID: req.WasID,
	}, nil
}

func (h *handlers) place(ctx context.Context, id, under string) error {
	by, err := h.angelus.topologyPrincipal(ctx)
	if err != nil {
		return err
	}
	_, err = h.angelus.topology().Apply(ctx, by, hierarchy.Plan{Op: hierarchy.Place, Node: id, To: under})
	return err
}

func (h *handlers) promote(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.PromoteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	by, err := h.angelus.topologyPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.angelus.topology().Apply(ctx, by, hierarchy.Plan{Op: hierarchy.Promote, Node: req.FigaroID, Levels: req.Levels})
	if prop, ok := h.parkedProposal(ctx, err); ok {
		return rpc.PromoteResponse{FigaroID: req.FigaroID, Parked: prop}, nil
	}
	if errors.Is(err, hierarchy.ErrUnsupported) {
		return rpc.PromoteResponse{FigaroID: req.FigaroID, Unsupported: true}, nil
	}
	if errors.Is(err, hierarchy.ErrAtTop) {
		return rpc.PromoteResponse{FigaroID: req.FigaroID, Climbed: 0, AtStump: true}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("promote %q: %w", req.FigaroID, err)
	}
	slog.Info("promoted figaro", "trunk", req.FigaroID, "levels", req.Levels, "climbed", p.Levels)
	return rpc.PromoteResponse{FigaroID: req.FigaroID, Climbed: p.Levels}, nil
}

func (h *handlers) demote(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.DemoteRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	by, err := h.angelus.topologyPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.angelus.topology().Apply(ctx, by, hierarchy.Plan{Op: hierarchy.Demote, Node: req.FigaroID, To: req.Prefer})
	if prop, ok := h.parkedProposal(ctx, err); ok {
		return rpc.DemoteResponse{FigaroID: req.FigaroID, Under: p.To, Parked: prop}, nil
	}
	if errors.Is(err, hierarchy.ErrUnsupported) {
		return rpc.DemoteResponse{FigaroID: req.FigaroID, Unsupported: true}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("demote %q: %w", req.FigaroID, err)
	}
	under := p.To
	slog.Info("demoted figaro", "trunk", req.FigaroID, "under", under)
	return rpc.DemoteResponse{FigaroID: req.FigaroID, Under: under, Preferred: under != "" && under == req.Prefer}, nil
}

func (h *handlers) kill(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.KillRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if req.Recursive && req.KeepChildren {
		return nil, fmt.Errorf("kill %s: -r takes its children and --keep-children lifts them; ask for one", req.FigaroID)
	}
	by, err := h.angelus.topologyPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	asked := hierarchy.Plan{Op: hierarchy.Kill, Node: req.FigaroID, Recursive: true}
	if req.KeepChildren {
		asked = hierarchy.Plan{Op: hierarchy.Kill, Node: req.FigaroID, Lift: true}
	}
	checked, err := h.angelus.topology().Check(ctx, by, asked)
	err = deleteSetChanged(err)
	if err != nil {
		switch {
		case req.Recursive:
			return nil, fmt.Errorf("kill -r %s: %w", req.FigaroID, err)
		case req.KeepChildren:
			return nil, fmt.Errorf("kill --keep-children %s: %w", req.FigaroID, err)
		}
		return nil, fmt.Errorf("kill %s: %w", req.FigaroID, err)
	}
	if h.afterKillCheck != nil {
		h.afterKillCheck()
	}
	grace := time.Duration(req.GraceMS) * time.Millisecond
	if box, ok := h.config.BoxPurgeGrace(h.angelus.sandboxOf(req.FigaroID)); ok {
		if grace <= 0 || grace > box {
			grace = box
		}
	} else if grace <= 0 {
		grace = h.config.KillGrace()
	}
	if !req.Now && grace > 0 {
		if until, ok, err := h.reapKilled(ctx, by, req.FigaroID, req.Recursive, req.KeepChildren, time.Now().Add(grace), checked.Delete); ok || err != nil {
			if err != nil {
				return nil, err
			}
			return rpc.KillResponse{OK: true, PurgeAt: until.UnixMilli(), Roles: h.angelus.rolesIn(checked.Delete), Lifted: liftedIn(checked), Reaped: orSelf(checked.Delete, req.FigaroID)}, nil
		}
	}
	expect := checked.Delete
	if !req.Recursive && len(expect) > 1 {
		expect = nil
	}
	if group, reaped := h.angelus.reapedGroupOf(req.FigaroID); reaped && !sameMembers(group, checked.Delete) {
		return nil, fmt.Errorf("%w: %s was reaped with %v, and a delete of it now takes %v; nothing was purged", store.ErrDeleteSetChanged, req.FigaroID, group, checked.Delete)
	}
	roles := h.angelus.rolesIn(checked.Delete)
	removed, err := h.removeAria(ctx, by, hierarchy.Plan{Op: hierarchy.Kill, Node: req.FigaroID, Recursive: req.Recursive, Lift: req.KeepChildren, Delete: expect})
	if err != nil {
		return nil, err
	}
	return rpc.KillResponse{OK: true, Removed: removed, Roles: roles, Lifted: liftedIn(checked)}, nil
}

func liftedIn(p hierarchy.Plan) []string {
	var out []string
	for _, m := range p.Moves {
		if m.Lift {
			out = append(out, m.Node)
		}
	}
	return out
}

// RemoveAria is the whole of a deletion: agent, background sessions, bytes, endpoint.
func (h *handlers) RemoveAria(id string, recursive bool) error {
	op := hierarchy.Discard
	if recursive {
		op = h.systemDelete(id)
	}
	_, err := h.removeAria(context.Background(), hierarchy.System, hierarchy.Plan{Op: op, Node: id, Recursive: recursive})
	return err
}

func (h *handlers) RemoveAriaIf(id string, expect []string) error {
	_, err := h.removeAria(context.Background(), hierarchy.System, hierarchy.Plan{Op: h.systemDelete(id), Node: id, Recursive: true, Delete: expect})
	return err
}

func (h *handlers) systemDelete(id string) hierarchy.Op {
	if h.angelus.isReapedRoot(id) {
		return hierarchy.Purge
	}
	return hierarchy.Sweep
}

func (h *handlers) removeAria(ctx context.Context, by hierarchy.Principal, asked hierarchy.Plan) ([]string, error) {
	id := asked.Node
	var plan hierarchy.Plan
	if h.angelus.Backend != nil {
		var err error
		plan, err = h.angelus.topology().Check(ctx, by, asked)
		if err != nil {
			return nil, deleteSetChanged(err)
		}
	}
	if h.angelus.Registry.Get(id) != nil {
		if err := h.angelus.Registry.Kill(id); err != nil {
			return nil, err
		}
	}

	// Unconditional: a hibernated aria has no live agent and still owns running children.
	if n := h.killSessions(id); n > 0 {
		slog.Info("killed aria sessions", "id", id, "sessions", n)
	}
	for _, n := range plan.Delete {
		if n != id {
			h.stopAria(n)
		}
	}

	var removed []string
	if h.angelus.Backend != nil {
		asked.Delete, asked.Moves = plan.Delete, plan.Moves
		applied, err := h.angelus.topology().Apply(ctx, by, asked)
		if err != nil {
			return nil, deleteSetChanged(err)
		}
		removed = orSelf(applied.Delete, id)
		for _, n := range applied.Delete {
			h.angelus.held.release(n)
			if err := h.angelus.Spool.Remove(n); err != nil {
				slog.Warn("spool remove", "id", n, "err", err)
			}
		}
	}

	// The endpoint outlives the agent, not the aria; this EOF must not happen on hibernate.
	for _, n := range orSelf(removed, id) {
		if hb := h.angelus.Hubs.drop(n); hb != nil {
			hb.Close()
		}
	}

	slog.Info("killed figaro", "id", id, "op", asked.Op.String(), "taken", removed)
	return removed, nil
}

func mintedBy(ctx context.Context) store.BirthOption {
	return store.MintedBy(authz.PrincipalText(authz.PrincipalOf(ctx)))
}

func orSelf(ids []string, id string) []string {
	if len(ids) == 0 {
		return []string{id}
	}
	return ids
}

func sameMembers(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, id := range a {
		seen[id]++
	}
	for _, id := range b {
		if seen[id] == 0 {
			return false
		}
		seen[id]--
	}
	return true
}
