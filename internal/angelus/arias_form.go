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

type ariasWatch struct {
	build sync.Mutex
	ctx   context.Context
	form  *store.Form
	hub   *ariaHub
	used  time.Time
	print uint64
	built bool
}

type ariasForms struct {
	mu      sync.Mutex
	watches map[string]*ariasWatch
	ticking bool
}

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
		w = &ariasWatch{ctx: context.WithoutCancel(ctx), form: store.NewMemForm()}
		w.hub = h.newAriasHub(w)
		w.form.OnCommit(func(c store.Commit) {
			_ = w.hub.Notify(rpc.MethodFormDelta, rpc.FormDelta{
				Schema: rpc.FormDeltaSchema, AriaID: rpc.AriasFormID, Version: c.Version,
				Patch: c.Patch, At: time.Now().UnixMilli(),
			})
		})
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

func (h *handlers) newAriasHub(w *ariasWatch) *ariaHub {
	hb := newAriaHub(rpc.AriasFormID)
	hb.kind = kindFormNode
	hb.read = func(_ context.Context, _ string, method string, params json.RawMessage) (any, bool, error) {
		if method != rpc.MethodForm {
			return nil, true, fmt.Errorf("%s is computed for each caller: %s does not apply, read it with %s", rpc.AriasFormID, method, rpc.MethodForm)
		}
		var req rpc.FormRequest
		if len(params) > 0 {
			_ = json.Unmarshal(params, &req)
		}
		if req.Intrinsic != "" && req.Intrinsic != figaro.IntrinsicState {
			return nil, true, fmt.Errorf("%s has no intrinsic %q", rpc.AriasFormID, req.Intrinsic)
		}
		return figaro.FormResponseOf(w.form.Since(req.Since), ""), true, nil
	}
	hb.access = func(context.Context, string, json.RawMessage) *rpc.FormAccess {
		return &rpc.FormAccess{Mode: rpc.FormReadOnly}
	}
	hb.view = formViewOf
	return hb
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
			w.hub.drop()
			continue
		}
		if len(w.hub.targets()) > 0 {
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
	next := ariasSnapshot(rows, peers)
	cur, _ := w.form.Snapshot()
	if patch := next.Diff(cur); !patch.IsIdentity() {
		_, _, _ = w.form.ApplyEffect(patch, 0)
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
		parent, ok := byID[r.Parent]
		if r.Parent == "" || !ok || seen[r.ID] {
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

func (h *handlers) ariasMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	w := h.ariasWatchFor(ctx)
	hb := w.hub
	switch method {
	case rpc.MethodFollow:
		if f := followerOf(ctx); f != nil {
			f.follow(hb)
		}
		return rpc.FollowResponse{OK: true, FigaroID: rpc.AriasFormID}, nil
	case rpc.MethodForm:
		if f := followerOf(ctx); f != nil && rpc.MethodFollows(method) {
			f.follow(hb)
		}
		h.rebuildArias(w)
		return hb.route(ctx, method, params)
	}
	return nil, fmt.Errorf("%s is computed for each caller and read-only: %s does not apply", rpc.AriasFormID, method)
}
