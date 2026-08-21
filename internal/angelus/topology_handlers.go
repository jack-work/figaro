package angelus

// Birth, fork, promote, import and removal: the handlers that change the SHAPE
// of the store rather than the contents of one aria.
//
// Split out of protocol.go, which had grown to 2,011 lines and answered every
// question at once. Same package, same behaviour: only the reader's job
// changes. plans/api-coherence.md step 5.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	figOtel "github.com/jack-work/figaro/internal/otel"
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

	// Resolve the outfit name. Empty request → configured default →
	// typed JSON-RPC error so the client can drive first-run setup.
	loaded, _ := h.settings()

	// TWO PATCHES, and which is which is the whole economy of this.
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

	// The child carries only what is per-aria: the caller's KEYS (-S/-D) and
	// the runtime fill-ins. The names are in the parent now.
	dress, err := h.dress(nil, req.Patch)
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

	backend := h.angelus.Backend

	// The form channel is the durable truth; cbState is the
	// in-memory hot view. System mints all ids.
	cbState, _ := form.Open("")
	var id string

	{
		// Ensure the DEFAULT FORM (stumps are legacy), then fork it: `fig
		// new` is bind-the-default-form, and this reuse is what shares one
		// rendered prefix, and one warm provider cache, across every
		// aria on the same outfit. The hash optimization IS the cache.
		formID, serr := h.birthParent(backend, stumpPatch, outfitName, named)
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
		if _, aerr := backend.ApplyFormPrivileged(id, convBootPatch(id, cwd)); aerr != nil {
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
		Settings:        loaded,
		UICache:         h.angelus.UICache,
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

func (h *handlers) forkPointOf(ariaID string, turn uint64) (uint64, error) {
	if turn == 0 {
		return 0, nil // head fork
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

func (h *handlers) fork(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ForkRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
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

// importAria restores an exported aria as a NEW conversation.
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
	id, err := h.angelus.Backend.CreateConversation(outfitID)
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
	// The form last, and as ONE patch: it is the aria's settled state,
	// not a history of how it got there. aria_id is re-stamped because the
	// exported board carries the id it had in the store it came from: the
	// same re-stamp a fork does, for the same reason.
	patch := req.Form
	if patch.Set == nil {
		patch.Set = map[string]json.RawMessage{}
	}
	// The study set is NOT restored by copying the key. `system.studies` is
	// system-managed precisely because each entry is refcounted on a shared
	// libretto: a board that names a study nothing counted is §12.2.2's
	// unrecoverable direction. So it is lifted out of the imported patch and
	// replayed through the VERB below, which retains as it declares.
	var studies []string
	if raw, ok := patch.Set[figaro.StudiesKey]; ok {
		if err := json.Unmarshal(raw, &studies); err != nil {
			return nil, fmt.Errorf("import: %s: %w", figaro.StudiesKey, err)
		}
		delete(patch.Set, figaro.StudiesKey)
	}
	if b, mErr := json.Marshal(id); mErr == nil {
		patch.Set["aria_id"] = b
	}
	if _, err := h.angelus.Backend.ApplyForm(id, patch); err != nil {
		return nil, fmt.Errorf("import: form: %w", err)
	}
	// IMPORT IS A REFCOUNT PARTICIPANT (durable-forms §12.2.2), and it pays
	// by studying rather than by declaring: each id goes through the verb,
	// which mints the libretto, seeds it and retains it. An import that
	// names a form this store does not have is not an error -- the libretto
	// holds an empty copy and starts following if that form ever arrives.
	for _, formID := range studies {
		if _, err := studyThroughStore(h.angelus.Backend, id, formID, false); err != nil {
			slog.Warn("import: could not restore a study",
				"aria", id, "form", formID, "err", err)
		}
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
