package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/navstack"
	"github.com/jack-work/figaro/internal/primitive"
)

const (
	navSaveEvery   = 2 * time.Second
	navBackupEvery = time.Minute
	navFileName    = "navstack.json"
)

var navClock = primitive.Clock{StartOf: pidStartTime, Alive: isAlive}

type navForms struct {
	mu    sync.Mutex
	forms map[int]callerForm
}

func (h *handlers) startNav() {
	path := ""
	if h.angelus.RuntimeDir != "" {
		path = filepath.Join(h.angelus.RuntimeDir, navFileName)
	}
	h.nav = primitive.New(path, navClock, navstack.History.Clone, h.navChanged)
}

func (hs *Handlers) LoadNav() {
	if err := hs.h.nav.Load(); err != nil {
		slog.Warn("navigation stacks", "err", err)
	}
}

func (hs *Handlers) PersistNav(done <-chan struct{}) {
	hs.h.nav.Persist(done, navSaveEvery, navBackupEvery)
}

func (h *handlers) navPush(pid int, id string) {
	_, _ = h.nav.Update(pid, func(s *navstack.History) error {
		s.Push(id)
		return nil
	})
}

func (h *handlers) hop(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.HopRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if req.PID <= 0 {
		return nil, fmt.Errorf("hop: no shell (pid %d)", req.PID)
	}
	var resp rpc.HopResponse
	_, err := h.nav.Update(req.PID, func(s *navstack.History) error {
		bound, _, _ := h.angelus.Registry.Resolve(req.PID)
		if bound != "" && bound != s.Current() {
			s.Push(bound)
		}
		land, dropped, ok := s.Hop(req.Steps, bound == "", h.navFate)
		for _, d := range dropped {
			resp.Dropped = append(resp.Dropped, rpc.HopDropped{ID: d.ID, Why: d.Why})
		}
		if ok {
			if err := h.angelus.Registry.Bind(req.PID, land, 0); err != nil {
				return err
			}
			resp.FigaroID, resp.Moved = land, true
		}
		resp.Pos, resp.Total = s.Where()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (h *handlers) navFate(id string) string {
	if node, _ := rpc.SplitNode(id); node != "" && !h.isSelf(node) {
		return ""
	}
	if err := h.requireAria(id); err != nil {
		return "gone"
	}
	if h.angelus.Backend != nil {
		if meta, _ := h.angelus.Backend.Meta(id); meta != nil && meta.ReapedAtMS > 0 {
			return "reaped"
		}
	}
	return ""
}

func navName(id string) string {
	node, bare := rpc.SplitNode(id)
	name := bare
	if w := handle.Words(bare, handle.MinWords); w != "" {
		name = w
	}
	if node != "" {
		return node[:min(rpc.NodeShortLen, len(node))] + rpc.NodeSeparator + name
	}
	return name
}

func navFormPID(id string) (int, bool) {
	rest, ok := strings.CutPrefix(id, rpc.NavFormPrefix)
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(rest)
	return pid, err == nil && pid > 0 && strconv.Itoa(pid) == rest
}

func isNavFormID(id string) bool {
	_, ok := navFormPID(id)
	return ok
}

func (h *handlers) navForm(pid int) (callerForm, bool) {
	h.navForms.mu.Lock()
	defer h.navForms.mu.Unlock()
	if f, ok := h.navForms.forms[pid]; ok {
		return f, false
	}
	if h.navForms.forms == nil {
		h.navForms.forms = map[int]callerForm{}
	}
	f := newCallerForm(rpc.NavFormID(pid))
	h.navForms.forms[pid] = f
	return f, true
}

func (h *handlers) navChanged(pid int, s navstack.History) {
	h.navForms.mu.Lock()
	f, ok := h.navForms.forms[pid]
	h.navForms.mu.Unlock()
	if ok {
		h.fillNavForm(f, s)
	}
}

func (h *handlers) fillNavForm(f callerForm, s navstack.History) {
	keys := map[string]json.RawMessage{}
	put := func(k string, v any) {
		raw, _ := json.Marshal(v)
		keys[k] = raw
	}
	pos, total := s.Where()
	put("pos", pos)
	put("total", total)
	for i, id := range s.IDs {
		base := fmt.Sprintf("entries.%02d.", i+1)
		put(base+"id", id)
		put(base+"n", i+1)
		put(base+"name", navName(id))
		if i == s.Pos {
			put(base+"here", "true")
		}
		if h.angelus.Backend == nil || strings.Contains(id, rpc.NodeSeparator) {
			continue
		}
		if snap, err := h.angelus.Backend.FormState(id); err == nil {
			if m := snap.Lookup("mantra"); m != nil && *m != "" {
				put(base+"mantra", *m)
			}
		}
	}
	cur, _ := f.form.Snapshot()
	next := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, keys, nil))
	if patch := next.Diff(cur); !patch.IsIdentity() {
		_, _, _ = f.form.ApplyEffect(patch, 0)
	}
}

func (h *handlers) navFormMethod(ctx context.Context, id, method string, params json.RawMessage) (any, error) {
	if err := h.allowed(ctx, rpc.VerbAdmin, authz.Daemon); err != nil {
		return nil, authz.NotFound(method, id)
	}
	pid, _ := navFormPID(id)
	f, fresh := h.navForm(pid)
	if fresh {
		h.fillNavForm(f, h.nav.Get(pid))
	}
	switch method {
	case rpc.MethodFollow:
		ch, err := followChannels(params)
		if err != nil {
			return nil, err
		}
		if fl := followerOf(ctx); fl != nil {
			fl.follow(f.hub, ch)
		}
		return rpc.FollowResponse{OK: true, FigaroID: id}, nil
	case rpc.MethodForm:
		if fl := followerOf(ctx); fl != nil && rpc.MethodFollows(method) {
			fl.follow(f.hub, allChannels)
		}
		return f.hub.route(ctx, method, params)
	}
	return nil, fmt.Errorf("%s is this shell's navigation history and read-only: %s does not apply", id, method)
}
