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
	"strings"
	"sync"
	"time"

	"github.com/jack-work/jkauthz"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/turns"
)

const (
	successionHandoff = "handoff"
	successionRamping = "ramping"

	successionPolicyKey      = "succession-policy"
	successionDefaultHandoff = "handoff"
	successionQuoteCap       = 16 << 10
	successionFailedTTL      = 24 * time.Hour
	succeededPrefix          = "SUCCEEDED: "
)

type successionRecord struct {
	Old        string   `json:"old"`
	Phase      string   `json:"phase"`
	Successor  string   `json:"successor,omitempty"`
	Roles      []string `json:"roles,omitempty"`
	DeadlineMS int64    `json:"deadline"`
	Purge      bool     `json:"purge,omitempty"`
}

type successions struct {
	run  sync.Mutex
	mu   sync.Mutex
	once sync.Once
	path string
	recs map[string]*successionRecord
	form *store.Form
	now  func() time.Time
}

func newSuccessions(path string) *successions {
	return &successions{path: path, recs: map[string]*successionRecord{}, form: store.NewMemForm(), now: time.Now}
}

func (s *successions) load() {
	s.once.Do(func() {
		if s.path == "" {
			return
		}
		raw, err := os.ReadFile(s.path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				slog.Error("successions: read", "path", s.path, "err", err)
			}
			return
		}
		var recs []*successionRecord
		if err := json.Unmarshal(raw, &recs); err != nil {
			slog.Error("successions: decode", "path", s.path, "err", err)
			return
		}
		s.mu.Lock()
		for _, r := range recs {
			s.recs[r.Old] = r
		}
		s.mu.Unlock()
		s.publish()
	})
}

func (s *successions) of(old string) *successionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.recs[old]; r != nil {
		c := *r
		return &c
	}
	return nil
}

func (s *successions) succeededBy(successor string) *successionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.recs {
		if r.Successor == successor && r.Phase == successionRamping {
			c := *r
			return &c
		}
	}
	return nil
}

func (s *successions) all() []successionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]successionRecord, 0, len(s.recs))
	for _, r := range s.recs {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Old < out[j].Old })
	return out
}

func (s *successions) put(r successionRecord) error {
	s.mu.Lock()
	s.recs[r.Old] = &r
	s.mu.Unlock()
	return s.save()
}

func (s *successions) remove(old string) error {
	s.mu.Lock()
	delete(s.recs, old)
	s.mu.Unlock()
	return s.save()
}

func (s *successions) save() error {
	s.publish()
	if s.path == "" {
		return nil
	}
	raw, err := json.Marshal(s.all())
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *successions) publish() {
	keys := map[string]json.RawMessage{}
	put := func(k string, v any) {
		raw, _ := json.Marshal(v)
		keys[k] = raw
	}
	for _, r := range s.all() {
		base := "items." + r.Old + "."
		put(base+"phase", r.Phase)
		put(base+"deadline", r.DeadlineMS)
		if r.Successor != "" {
			put(base+"successor", r.Successor)
		}
		if len(r.Roles) > 0 {
			put(base+"roles", r.Roles)
		}
	}
	cur, _ := s.form.Snapshot()
	next := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, keys, nil))
	if patch := next.Diff(cur); !patch.IsIdentity() {
		_, _, _ = s.form.ApplyEffect(patch, 0)
	}
}

func (h *handlers) successionsForm() *store.Form {
	h.succ.load()
	return h.succ.form
}

type successionParty interface {
	SubmitSuccessionPrompt(from, text string, handoff bool) error
	ApplySuccession(patch form.Patch) error
	RetireSucceeded(ttl time.Duration, mantra string) error
	Drop(formID string) ([]string, error)
}

func (h *handlers) ownerContext() context.Context {
	ctx := authz.WithSubject(context.Background(), jkauthz.Subject{Principal: h.angelus.owner()})
	return authz.WithDoor(ctx, authz.DoorLocal)
}

func (h *handlers) successionParty(ctx context.Context, id string) (successionParty, error) {
	f, err := h.restoreByID(ctx, id, "succession")
	if err != nil {
		return nil, err
	}
	p, ok := f.(successionParty)
	if !ok {
		return nil, fmt.Errorf("%s takes no succession", id)
	}
	return p, nil
}

func (h *handlers) turnEnded(e figaro.TurnEnd) {
	h.succ.load()
	if h.succ.succeededBy(e.ID) == nil && h.succ.of(e.ID) == nil && !e.AtThreshold() {
		return
	}
	go h.advanceSuccession(e)
}

func (h *handlers) advanceSuccession(e figaro.TurnEnd) {
	h.succ.run.Lock()
	defer h.succ.run.Unlock()
	ctx := h.ownerContext()
	if rec := h.succ.succeededBy(e.ID); rec != nil {
		if e.Failed {
			h.failSuccession(ctx, *rec, fmt.Sprintf("the first turn of successor %s failed: %s", e.ID, e.Reason))
			return
		}
		h.completeSuccession(ctx, *rec)
		return
	}
	if rec := h.succ.of(e.ID); rec != nil {
		if rec.Phase != successionHandoff || !e.Handoff {
			return
		}
		if e.Failed {
			h.failSuccession(ctx, *rec, "the final handoff turn failed: "+e.Reason)
			return
		}
		h.mintSuccessor(ctx, *rec)
		return
	}
	if e.AtThreshold() {
		h.beginSuccession(ctx, e)
	}
}

type heldRole struct {
	id, name, handoff, outfit string
}

func nested(snap form.Snapshot, table, field string) string {
	if v := snap.Lookup(table + "." + field); v != nil {
		return *v
	}
	raw, ok := snap.Get(table)
	if !ok {
		return ""
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(fields[field], &s)
	return s
}

func (h *handlers) rolesHeldBy(id string) ([]heldRole, error) {
	b := h.angelus.Backend
	var out []heldRole
	for _, f := range b.Forms() {
		if _, ok := store.SourceOfLibretto(f.ID); ok {
			continue
		}
		snap, err := b.FormState(f.ID)
		if err != nil {
			return nil, fmt.Errorf("role scan: form %s: %w", f.ID, err)
		}
		if v := snap.Lookup("target-aria"); v == nil || *v != id {
			continue
		}
		r := heldRole{id: f.ID, outfit: nested(snap, successionPolicyKey, "outfit")}
		for _, k := range []string{"name", "role-name"} {
			if v := snap.Lookup(k); v != nil && r.name == "" {
				r.name = *v
			}
		}
		key := nested(snap, successionPolicyKey, "handoff-key")
		if key == "" {
			key = successionDefaultHandoff
		}
		if v := snap.Lookup(key); v != nil {
			r.handoff = *v
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, nil
}

func successorOutfit(roles []heldRole) (string, error) {
	outfit, from := "", ""
	for _, r := range roles {
		switch {
		case r.outfit == "" || r.outfit == outfit:
		case outfit == "":
			outfit, from = r.outfit, r.id
		default:
			return "", fmt.Errorf("roles disagree on the successor's outfit: %s wants %q, %s wants %q", from, outfit, r.id, r.outfit)
		}
	}
	return outfit, nil
}

func describeRoles(roles []heldRole) string {
	if len(roles) == 0 {
		return "You hold no role."
	}
	var b strings.Builder
	for _, r := range roles {
		fmt.Fprintf(&b, "\n- %s", r.id)
		if r.name != "" {
			fmt.Fprintf(&b, " (%s)", r.name)
		}
		if r.handoff != "" {
			fmt.Fprintf(&b, ": handoff at %s", r.handoff)
		} else {
			b.WriteString(": no handoff key set; write one and set it on the role")
		}
	}
	return "Roles:" + b.String()
}

func roleIDs(roles []heldRole) []string {
	out := make([]string, len(roles))
	for i, r := range roles {
		out[i] = r.id
	}
	return out
}

func (h *handlers) retirementOf(id string) (store.Retirement, form.Snapshot) {
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return store.Retirement{}, form.Snapshot{}
	}
	r, _ := store.RetirementOf(snap)
	return r, snap
}

func (h *handlers) beginSuccession(ctx context.Context, e figaro.TurnEnd) {
	_, snap := h.retirementOf(e.ID)
	if snap.Has(store.SystemSuccessionFailedKey) || store.RetiredIn(snap) {
		return
	}
	rec := successionRecord{Old: e.ID, Phase: successionHandoff}
	roles, err := h.rolesHeldBy(e.ID)
	if err != nil {
		h.failSuccession(ctx, rec, err.Error())
		return
	}
	rec.Roles = roleIDs(roles)
	if _, err := successorOutfit(roles); err != nil {
		h.failSuccession(ctx, rec, err.Error())
		return
	}
	for _, r := range roles {
		if err := store.RequireCastTarget(h.angelus.Backend, r.id); err != nil {
			h.failSuccession(ctx, rec, fmt.Sprintf("role %s cannot be cast: %s", r.id, err))
			return
		}
	}
	old, err := h.successionParty(ctx, e.ID)
	if err != nil {
		h.failSuccession(ctx, rec, err.Error())
		return
	}
	rec.DeadlineMS = h.succ.now().Add(e.Retirement.SuccessionTimeout()).UnixMilli()
	if err := h.succ.put(rec); err != nil {
		h.failSuccession(ctx, rec, "record the succession: "+err.Error())
		return
	}
	text := fmt.Sprintf("Your context reached its retirement threshold (%d of %d input tokens). This is your final turn: write your handoff now and start nothing new. "+
		"If you were not yet told to start handing over, this is that notice too.\n%s\n"+
		"When this turn ends a successor is minted, cast into your roles and shown your last %d turns. You retire when its first turn ends.",
		e.Used, e.Threshold, describeRoles(roles), e.Retirement.QuotedTurns())
	if err := old.SubmitSuccessionPrompt(successionSender, text, true); err != nil {
		h.failSuccession(ctx, rec, "the final handoff turn could not be queued: "+err.Error())
		return
	}
	slog.Info("succession began", "aria", e.ID, "roles", rec.Roles, "context", e.Used, "threshold", e.Threshold)
}

const successionSender = "figaro"

func copied(snap form.Snapshot, keys map[string]json.RawMessage, names ...string) {
	for _, k := range names {
		if raw, ok := snap.Get(k); ok && string(raw) != "null" {
			keys[k] = raw
		}
	}
}

func (h *handlers) mintSuccessor(ctx context.Context, rec successionRecord) {
	r, snap := h.retirementOf(rec.Old)
	roles, err := h.rolesHeldBy(rec.Old)
	if err != nil {
		h.failSuccession(ctx, rec, err.Error())
		return
	}
	rec.Roles = roleIDs(roles)
	outfit, err := successorOutfit(roles)
	if err != nil {
		h.failSuccession(ctx, rec, err.Error())
		return
	}
	keys := map[string]json.RawMessage{store.SystemTTLKey: json.RawMessage(`"0"`)}
	copied(snap, keys, store.SystemRetirementKey, "system.scopes", "system.sandbox")
	if outfit == "" {
		if v := snap.Lookup("system.outfit_name"); v != nil {
			outfit = *v
		}
		copied(snap, keys, "system.provider", "system.model", "system.thinking_effort", "system.thinking_budget")
	}
	mantra := ""
	if v := snap.Lookup("mantra"); v != nil {
		mantra = *v
	}
	keys["mantra"], _ = json.Marshal("succeeding " + rec.Old[:min(8, len(rec.Old))] + ": " + mantra)
	patch := form.Build(form.Snapshot{}, keys, nil)
	req := rpc.CreateRequest{Patch: &patch}
	if outfit != "" {
		req.Outfits = strings.Split(outfit, ",")
	}
	if v := snap.Lookup("system.cwd"); v != nil {
		req.Cwd = *v
	}
	minted, err := h.mint(ctx, SuccessorMint{Predecessor: rec.Old, Roles: rec.Roles, Form: snap, Default: req})
	if err != nil {
		h.failSuccession(ctx, rec, "mint the successor: "+err.Error())
		return
	}
	rec.Successor, rec.Purge = minted.ID, minted.PurgePredecessor
	if err := h.placeBeside(ctx, rec.Successor, rec.Old); err != nil {
		h.discardSuccessor(ctx, rec.Successor)
		rec.Successor = ""
		h.failSuccession(ctx, rec, "place the successor under the predecessor's parent: "+err.Error())
		return
	}
	for i, role := range rec.Roles {
		if err := h.castSuccessor(ctx, rec.Successor, role); err != nil {
			why := fmt.Sprintf("cast into %s refused: %s", role, err)
			if back := h.repoint(ctx, rec.Roles[:i+1], rec.Old); back != "" {
				why += "; " + back
			} else {
				h.discardSuccessor(ctx, rec.Successor)
				rec.Successor = ""
			}
			h.failSuccession(ctx, rec, why)
			return
		}
	}
	rec.Phase = successionRamping
	rec.DeadlineMS = h.succ.now().Add(r.SuccessionTimeout()).UnixMilli()
	if err := h.succ.put(rec); err != nil {
		h.failSuccession(ctx, rec, "record the succession: "+err.Error())
		return
	}
	next, err := h.successionParty(ctx, rec.Successor)
	if err == nil {
		err = next.SubmitSuccessionPrompt(rec.Old, h.rampBrief(rec, r, roles, mantra), false)
	}
	if err != nil {
		h.failSuccession(ctx, rec, "the ramp prompt could not be queued: "+err.Error())
		return
	}
	slog.Info("successor minted and cast", "aria", rec.Old, "successor", rec.Successor, "roles", rec.Roles)
}

type SuccessorMint struct {
	Predecessor string
	Roles       []string
	Form        form.Snapshot
	Default     rpc.CreateRequest
}

type MintedSuccessor struct {
	ID               string
	PurgePredecessor bool
}

type SuccessorMinter func(ctx context.Context, m SuccessorMint) (minted MintedSuccessor, mine bool, err error)

func (h *handlers) mint(ctx context.Context, m SuccessorMint) (MintedSuccessor, error) {
	if minter := h.angelus.SuccessorMinter; minter != nil {
		if minted, mine, err := minter(ctx, m); mine || err != nil {
			if err == nil && minted.ID == "" {
				err = errors.New("the minter claimed the succession and minted nobody")
			}
			return minted, err
		}
	}
	raw, _ := json.Marshal(m.Default)
	out, err := h.create(ctx, raw)
	if err != nil {
		return MintedSuccessor{}, err
	}
	return MintedSuccessor{ID: out.(rpc.CreateResponse).FigaroID}, nil
}

func (h *handlers) castSuccessor(ctx context.Context, successor, role string) error {
	if h.successionCastHook != nil {
		if err := h.successionCastHook(role); err != nil {
			return err
		}
	}
	_, err := h.castForHub(ctx, successor, rpc.CastRequest{FormID: role})
	return err
}

func (h *handlers) placeBeside(ctx context.Context, id, sibling string) error {
	eng := h.angelus.Backend.HierarchyEngine()
	if eng == nil {
		return nil
	}
	want := eng.Overrides()[sibling]
	if stored, _ := eng.Stored(sibling); want == "" || want == stored {
		return nil
	}
	if have, _ := eng.Drawn(id); have == want {
		return nil
	}
	return h.place(ctx, id, want)
}

func (h *handlers) discardSuccessor(ctx context.Context, id string) {
	raw, _ := json.Marshal(rpc.KillRequest{FigaroID: id})
	if _, err := h.kill(ctx, raw); err != nil {
		slog.Error("succession: discard the unprompted successor", "successor", id, "err", err)
	}
}

func (h *handlers) repoint(ctx context.Context, roles []string, to string) string {
	var stuck []string
	for _, role := range roles {
		snap, err := h.angelus.Backend.FormState(role)
		if err == nil {
			if v := snap.Lookup("target-aria"); v != nil && *v == to {
				continue
			}
		}
		if _, err := h.castForHub(ctx, to, rpc.CastRequest{FormID: role}); err != nil {
			stuck = append(stuck, fmt.Sprintf("%s (%s)", role, err))
		}
	}
	if len(stuck) == 0 {
		return ""
	}
	return "could not point back at the predecessor: " + strings.Join(stuck, ", ")
}

func (h *handlers) rampBrief(rec successionRecord, r store.Retirement, roles []heldRole, mantra string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You succeed figaro %s (%q). It reached its context threshold and retires when this turn of yours ends; until then it still answers. "+
		"This turn is ramp-on only: read in, take over, start no new work.\n", rec.Old, mantra)
	if len(roles) > 0 {
		b.WriteString("You are already cast into its roles. " + describeRoles(roles) + "\n")
	} else {
		b.WriteString("It held no role.\n")
	}
	if r.Ramp != "" {
		fmt.Fprintf(&b, "Read %s first and follow it.\n", r.Ramp)
	}
	fmt.Fprintf(&b, "Its last %d turns (prose only; `figaro show %s -n %d` has the rest):\n\n%s",
		r.QuotedTurns(), rec.Old, r.QuotedTurns(), h.lastTurns(rec.Old, r.QuotedTurns()))
	return b.String()
}

func (h *handlers) lastTurns(id string, n int) string {
	log, err := h.angelus.Backend.OpenFigIR(id)
	if err != nil {
		return "(unreadable: " + err.Error() + ")"
	}
	entries := log.Read()
	msgs := make([]message.Message, len(entries))
	for i, e := range entries {
		msgs[i] = e.Payload
	}
	last := turns.StampIDs(msgs)
	var parts []string
	for _, m := range msgs {
		if uint64(n) < last && m.TurnID <= last-uint64(n) {
			continue
		}
		text := turns.Text(m)
		if text == "" || message.IsCeremonial(m) {
			continue
		}
		who := "them"
		if m.Role == message.RoleInput {
			who = "prompt"
		}
		parts = append(parts, fmt.Sprintf("[turn %d, %s]\n%s", m.TurnID, who, text))
	}
	out := strings.Join(parts, "\n\n")
	if len(out) > successionQuoteCap {
		out = "(earlier text cut)\n" + out[len(out)-successionQuoteCap:]
	}
	return out
}

func (h *handlers) completeSuccession(ctx context.Context, rec successionRecord) {
	if rec.Purge && h.purgePredecessor(ctx, rec) {
		return
	}
	r, snap := h.retirementOf(rec.Old)
	old, err := h.successionParty(ctx, rec.Old)
	if err != nil {
		h.failSuccession(ctx, rec, "retire the predecessor: "+err.Error())
		return
	}
	for _, role := range rec.Roles {
		if _, err := old.Drop(role); err != nil {
			slog.Warn("succession: the predecessor still studies a role", "aria", rec.Old, "role", role, "err", err)
		}
	}
	mantra := ""
	if v := snap.Lookup("mantra"); v != nil {
		mantra = *v
	}
	if !strings.HasPrefix(mantra, succeededPrefix) {
		mantra = succeededPrefix + mantra
	}
	if err := old.RetireSucceeded(r.TTL, mantra); err != nil {
		h.failSuccession(ctx, rec, "retire the predecessor: "+err.Error())
		return
	}
	if err := h.succ.remove(rec.Old); err != nil {
		slog.Error("succession: forget a finished succession", "aria", rec.Old, "err", err)
	}
	slog.Info("succession complete", "aria", rec.Old, "successor", rec.Successor, "roles", rec.Roles)
}

func (h *handlers) purgePredecessor(ctx context.Context, rec successionRecord) bool {
	raw, _ := json.Marshal(rpc.KillRequest{FigaroID: rec.Old})
	if _, err := h.kill(ctx, raw); err != nil {
		slog.Error("succession: the predecessor could not be purged and is retired instead", "aria", rec.Old, "successor", rec.Successor, "err", err)
		return false
	}
	if err := h.succ.remove(rec.Old); err != nil {
		slog.Error("succession: forget a finished succession", "aria", rec.Old, "err", err)
	}
	slog.Info("succession complete, predecessor purged", "aria", rec.Old, "successor", rec.Successor, "roles", rec.Roles)
	return true
}

func (h *handlers) failSuccession(ctx context.Context, rec successionRecord, why string) {
	r, _ := h.retirementOf(rec.Old)
	if rec.Successor != "" {
		if back := h.repoint(ctx, rec.Roles, rec.Old); back != "" {
			why += "; " + back
		}
		if next, err := h.successionParty(ctx, rec.Successor); err == nil {
			raw, _ := json.Marshal(successionFailedTTL.String())
			_ = next.ApplySuccession(form.Build(form.Snapshot{}, map[string]json.RawMessage{store.SystemTTLKey: raw}, nil))
		}
		why += fmt.Sprintf("; successor %s is kept for a day", rec.Successor)
	}
	slog.Error("succession failed: the aria stays unretired", "aria", rec.Old, "successor", rec.Successor, "why", why)
	if old, err := h.successionParty(ctx, rec.Old); err != nil {
		slog.Error("succession: record the failure", "aria", rec.Old, "err", err)
	} else {
		raw, _ := json.Marshal(why)
		if err := old.ApplySuccession(form.Build(form.Snapshot{}, map[string]json.RawMessage{store.SystemSuccessionFailedKey: raw}, nil)); err != nil {
			slog.Error("succession: record the failure", "aria", rec.Old, "err", err)
		}
	}
	if err := h.succ.remove(rec.Old); err != nil {
		slog.Error("succession: forget a failed succession", "aria", rec.Old, "err", err)
	}
	h.pageSuccession(ctx, rec, r, why)
}

func (h *handlers) pageSuccession(ctx context.Context, rec successionRecord, r store.Retirement, why string) {
	to, err := h.pageTarget(ctx, rec.Old, r)
	if err != nil {
		slog.Error("succession failed and nobody could be told", "aria", rec.Old, "err", err)
		return
	}
	p, err := h.successionParty(ctx, to)
	if err == nil {
		err = p.SubmitSuccessionPrompt(rec.Old, fmt.Sprintf(
			"figaro %s could not hand over at its context threshold and stays unretired, holding %s. Reason: %s. "+
				"It will not try again until %s is unset on its form.",
			rec.Old, orNone(rec.Roles), why, store.SystemSuccessionFailedKey), false)
	}
	if err != nil {
		slog.Error("succession failed and its page was not delivered", "aria", rec.Old, "to", to, "err", err)
	}
}

func orNone(roles []string) string {
	if len(roles) == 0 {
		return "no role"
	}
	return strings.Join(roles, ", ")
}

func (h *handlers) pageTarget(ctx context.Context, old string, r store.Retirement) (string, error) {
	ref := r.Page
	if ref == "" {
		if eng := h.angelus.Backend.HierarchyEngine(); eng != nil {
			ref, _ = eng.Drawn(old)
		}
		if ref == "" {
			return "", errors.New("system.retirement.page is unset and the aria is drawn under nothing")
		}
	} else {
		id, err := h.canonicalID(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("system.retirement.page %s: %w", ref, err)
		}
		ref = id
	}
	if snap, err := h.angelus.Backend.FormState(ref); err == nil {
		if v := snap.Lookup("target-aria"); v != nil && *v != "" {
			ref = *v
		}
	}
	if _, ok := h.angelus.Backend.Node(ref); !ok || ref == old {
		return "", fmt.Errorf("%s names nobody to tell", ref)
	}
	if meta, _ := h.angelus.Backend.Meta(ref); meta == nil {
		return "", fmt.Errorf("%s holds no figaro to tell", ref)
	}
	return ref, nil
}

func (h *handlers) sweepSuccessions() {
	h.succ.load()
	now := h.succ.now().UnixMilli()
	for _, rec := range h.succ.all() {
		if rec.DeadlineMS == 0 || rec.DeadlineMS > now {
			continue
		}
		h.succ.run.Lock()
		if cur := h.succ.of(rec.Old); cur != nil && cur.Phase == rec.Phase && cur.DeadlineMS <= now {
			what := "the final handoff turn"
			if cur.Phase == successionRamping {
				what = "the successor's first turn"
			}
			h.failSuccession(h.ownerContext(), *cur, what+" did not end before the timeout")
		}
		h.succ.run.Unlock()
	}
}

func SuccessionsPath(stateDir string) string { return filepath.Join(stateDir, "successions.json") }
