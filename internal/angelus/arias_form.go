package angelus

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
)

const (
	ariasTick = time.Second
	ariasIdle = 30 * time.Second
)

type callerForm struct {
	form *store.Form
	hub  *ariaHub
}

type ariasWatch struct {
	build sync.Mutex
	ctx   context.Context
	forms map[string]callerForm
	used  time.Time
	print uint64
	built bool
}

func (w *ariasWatch) followed() bool {
	for _, f := range w.forms {
		if len(f.hub.targets()) > 0 {
			return true
		}
	}
	return false
}

func (w *ariasWatch) drop() {
	for _, f := range w.forms {
		f.hub.drop()
	}
}

type ariasForms struct {
	mu      sync.Mutex
	watches map[string]*ariasWatch
	ticking bool
}

var callerFormIDs = []string{rpc.AriasFormID, rpc.TopologyFormID}

func isCallerFormID(id string) bool { return slices.Contains(callerFormIDs, id) }

func ariasKey(ctx context.Context) string {
	sub, _ := authz.SubjectOf(ctx)
	groups := slices.Clone(sub.Groups)
	slices.Sort(groups)
	return strings.Join(append([]string{string(authz.DoorOf(ctx)), authz.IssuerOf(ctx), authz.PrincipalText(authz.PrincipalOf(ctx)), sub.Principal.String()}, groups...), "\x00")
}

func (h *handlers) ariasWatchFor(ctx context.Context) *ariasWatch {
	key := ariasKey(ctx)
	a := &h.arias
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.watches == nil {
		a.watches = map[string]*ariasWatch{}
	}
	w, ok := a.watches[key]
	if !ok {
		w = newAriasWatch(context.WithoutCancel(ctx))
		h.rebuildArias(w)
		a.watches[key] = w
	}
	w.used = time.Now()
	if !a.ticking {
		a.ticking = true
		go h.tickArias()
	}
	return w
}

func newAriasWatch(ctx context.Context) *ariasWatch {
	w := &ariasWatch{ctx: ctx, forms: make(map[string]callerForm, len(callerFormIDs))}
	for _, id := range callerFormIDs {
		w.forms[id] = newCallerForm(id)
	}
	return w
}

func newCallerForm(id string) callerForm {
	f := callerForm{form: store.NewMemForm(), hub: newAriaHub(id)}
	f.form.OnCommit(func(c store.Commit) {
		_ = f.hub.Notify(rpc.MethodFormDelta, rpc.FormDelta{
			Schema: rpc.FormDeltaSchema, AriaID: id, Version: c.Version,
			Patch: c.Patch, At: time.Now().UnixMilli(),
		})
	})
	f.hub.kind = kindFormNode
	f.hub.read = func(_ context.Context, _ string, method string, params json.RawMessage) (any, bool, error) {
		if method != rpc.MethodForm {
			return nil, true, fmt.Errorf("%s is computed for each caller: %s does not apply, read it with %s", id, method, rpc.MethodForm)
		}
		var req rpc.FormRequest
		if len(params) > 0 {
			_ = json.Unmarshal(params, &req)
		}
		if req.Intrinsic != "" && req.Intrinsic != figaro.IntrinsicState {
			return nil, true, fmt.Errorf("%s has no intrinsic %q", id, req.Intrinsic)
		}
		return figaro.FormResponseOf(f.form.Since(req.Since), ""), true, nil
	}
	f.hub.access = func(context.Context, string, json.RawMessage) *rpc.FormAccess {
		return &rpc.FormAccess{Mode: rpc.FormReadOnly}
	}
	f.hub.view = formViewOf
	return f
}

func (h *handlers) tickArias() {
	t := time.NewTicker(ariasTick)
	defer t.Stop()
	for range t.C {
		if !h.ariasTick() {
			return
		}
	}
}

func (h *handlers) ariasTick() bool {
	a := &h.arias
	a.mu.Lock()
	var live []*ariasWatch
	for key, w := range a.watches {
		if !h.ariasMayWatch(w) {
			delete(a.watches, key)
			w.drop()
			continue
		}
		if w.followed() {
			w.used = time.Now()
			live = append(live, w)
			continue
		}
		if time.Since(w.used) > ariasIdle {
			delete(a.watches, key)
		}
	}
	if len(a.watches) == 0 {
		a.ticking = false
		a.mu.Unlock()
		return false
	}
	a.mu.Unlock()
	for _, w := range live {
		h.rebuildArias(w)
	}
	return true
}

func (h *handlers) ariasMayWatch(w *ariasWatch) bool {
	sub, err := authz.SubjectOf(w.ctx)
	if err != nil {
		return false
	}
	if h.angelus.Policies == nil {
		return h.passesUnpoliced(w.ctx, sub)
	}
	return h.angelus.Policies.HoldsRole(sub)
}

func (h *handlers) rebuildArias(w *ariasWatch) {
	w.build.Lock()
	defer w.build.Unlock()
	rows, peers := h.ariasRows(w.ctx)
	print := ariasPrint(rows, peers)
	if w.built && print == w.print {
		return
	}
	w.print, w.built = print, true
	for id, next := range map[string]form.Snapshot{
		rpc.AriasFormID:    ariasSnapshot(rows, peers),
		rpc.TopologyFormID: topologySnapshot(rows),
	} {
		f := w.forms[id].form
		cur, _ := f.Snapshot()
		if patch := next.Diff(cur); !patch.IsIdentity() {
			_, _, _ = f.ApplyEffect(patch, 0)
		}
	}
}

func (h *handlers) ariasRows(ctx context.Context) ([]rpc.FigaroInfoResponse, int) {
	peers := 0
	if h.fed != nil {
		peers = len(h.fed.members.Nodes())
	}
	res, err := h.list(ctx, nil)
	if err != nil {
		return nil, peers
	}
	all := res.(rpc.ListResponse).Figaros
	rows := all[:0]
	for i := range all {
		if all[i].ReapedAt == 0 {
			rows = append(rows, all[i])
		}
	}
	return rows, peers
}

func ariasPrint(rows []rpc.FigaroInfoResponse, peers int) uint64 {
	f := fnv.New64a()
	var num [8]byte
	putInt := func(n int64) {
		binary.LittleEndian.PutUint64(num[:], uint64(n))
		_, _ = f.Write(num[:])
	}
	putStr := func(s string) {
		putInt(int64(len(s)))
		_, _ = f.Write([]byte(s))
	}
	putInt(int64(peers))
	for _, r := range rows {
		putStr(r.ID)
		putStr(r.Handle)
		putStr(r.Parent)
		putStr(r.Present)
		putStr(r.State)
		putStr(r.Mantra)
		putStr(r.Cwd)
		putInt(int64(r.ContextTokens))
		putInt(int64(r.ContextLimit))
		putInt(r.LastActive)
	}
	return f.Sum64()
}

func ariasSnapshot(rows []rpc.FigaroInfoResponse, peers int) form.Snapshot {
	keys := map[string]json.RawMessage{}
	put := func(k string, v any) {
		raw, _ := json.Marshal(v)
		keys[k] = raw
	}
	if peers > 0 {
		put("peers", peers)
	}
	byID := make(map[string]*rpc.FigaroInfoResponse, len(rows))
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}
	label := func(r *rpc.FigaroInfoResponse) string {
		if r.Handle != "" {
			return r.Handle
		}
		return r.ID
	}
	var place func(r *rpc.FigaroInfoResponse, seen map[string]bool) string
	place = func(r *rpc.FigaroInfoResponse, seen map[string]bool) string {
		up := drawnUnder(r)
		parent, ok := byID[up]
		if up == "" || !ok || seen[r.ID] {
			return "arias." + label(r)
		}
		seen[r.ID] = true
		return place(parent, seen) + ".forks." + label(r)
	}
	for i := range rows {
		r := &rows[i]
		base := place(r, map[string]bool{}) + "."
		put(base+"id", r.ID)
		put(base+"state", r.State)
		if _, ok := byID[r.Parent]; ok {
			put(base+"parent", r.Parent)
		}
		if r.Mantra != "" {
			put(base+"mantra", r.Mantra)
		}
		if r.ContextTokens > 0 {
			put(base+"ctx", r.ContextTokens)
		}
		if r.ContextLimit > 0 {
			put(base+"ctx_limit", r.ContextLimit)
		}
		if r.LastActive > 0 {
			put(base+"active_at", r.LastActive)
		}
		if r.Cwd != "" {
			put(base+"cwd", r.Cwd)
		}
	}
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, keys, nil))
}

func (h *handlers) callerFormMethod(ctx context.Context, id, method string, params json.RawMessage) (any, error) {
	w := h.ariasWatchFor(ctx)
	hb := w.forms[id].hub
	switch method {
	case rpc.MethodFollow:
		if f := followerOf(ctx); f != nil {
			f.follow(hb)
		}
		return rpc.FollowResponse{OK: true, FigaroID: id}, nil
	case rpc.MethodForm:
		if f := followerOf(ctx); f != nil && rpc.MethodFollows(method) {
			f.follow(hb)
		}
		h.rebuildArias(w)
		return hb.route(ctx, method, params)
	case rpc.MethodSet:
		if id == rpc.TopologyFormID {
			h.rebuildArias(w)
			res, err := h.arrange(ctx, w.forms[id].form, params)
			if err != nil {
				return nil, err
			}
			if res.Outcome == rpc.OutcomeApplied {
				h.rebuildArias(w)
				res.Version = w.forms[id].form.Read().Version
			}
			return res, nil
		}
	}
	return nil, fmt.Errorf("%s is computed for each caller and read-only: %s does not apply", id, method)
}

func drawnUnder(r *rpc.FigaroInfoResponse) string {
	if r.Present != "" {
		return r.Present
	}
	return r.Parent
}
