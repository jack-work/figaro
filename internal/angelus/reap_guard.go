package angelus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

var errBeingReaped = errors.New("it is being reaped")

type reapGuard struct {
	mu      sync.Mutex
	sealed  map[string]bool
	holding map[string]int
}

func (g *reapGuard) enter(ids ...string) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, id := range ids {
		if g.sealed[id] {
			return nil, fmt.Errorf("%s: %w; try again", id, errBeingReaped)
		}
	}
	if g.holding == nil {
		g.holding = map[string]int{}
	}
	for _, id := range ids {
		g.holding[id]++
	}
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, id := range ids {
			if g.holding[id]--; g.holding[id] <= 0 {
				delete(g.holding, id)
			}
		}
	}, nil
}

func (g *reapGuard) seal(ids []string) (func(), string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, id := range ids {
		if g.holding[id] > 0 || g.sealed[id] {
			return nil, id
		}
	}
	if g.sealed == nil {
		g.sealed = map[string]bool{}
	}
	for _, id := range ids {
		g.sealed[id] = true
	}
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, id := range ids {
			delete(g.sealed, id)
		}
	}, ""
}

func roleTargetsIn(method string, params json.RawMessage) []string {
	if !bytes.Contains(params, []byte("target-aria")) && !bytes.ContainsRune(params, '\\') {
		return nil
	}
	var patch *form.Patch
	switch method {
	case rpc.MethodSet:
		var req rpc.SetRequest
		if json.Unmarshal(params, &req) == nil {
			patch = &req.Patch
		}
	case rpc.MethodQua:
		var req rpc.QuaRequest
		if json.Unmarshal(params, &req) == nil && req.Form != nil {
			patch = req.Form.Patch
		}
	}
	if patch == nil {
		return nil
	}
	return roleTargetsOf(*patch)
}

func roleTargetsOf(patch form.Patch) []string {
	var out []string
	if raw, ok := (form.Snapshot{}).Apply(patch).Get("target-aria"); ok {
		var id string
		if json.Unmarshal(raw, &id) == nil && id != "" {
			out = append(out, id)
		}
	}
	return out
}
