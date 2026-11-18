package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/jkrpc"
)

func (h *handlers) settingLineages(set jkrpc.HandlerFunc) jkrpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		if !bytes.Contains(params, []byte(`"lineage"`)) {
			return set(ctx, params)
		}
		var req rpc.SetRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		if !req.Lineage {
			return set(ctx, params)
		}
		if err := h.refuseLineageSet(ctx, params); err != nil {
			return nil, err
		}
		id := rpc.AriaOf(params)
		req.Lineage = false
		root, err := setThrough(ctx, set, id, req)
		if err != nil {
			return nil, err
		}
		req.Assert, req.IfVersion, req.Client, req.Seq = false, 0, "", 0
		for _, kid := range h.drawnBelow(id) {
			row := rpc.LineageSet{FigaroID: kid}
			if resp, err := setThrough(ctx, set, kid, req); err != nil {
				row.Refused = err.Error()
			} else {
				row.Outcome = resp.Outcome
			}
			root.Lineage = append(root.Lineage, row)
		}
		return root, nil
	}
}

func (h *handlers) refuseLineageSet(ctx context.Context, params json.RawMessage) error {
	if !h.ownerItself(ctx, params) {
		return errors.New("set --lineage is the owner's alone: a figaro or a box sets one figaro at a time")
	}
	return nil
}

func (h *handlers) ownerItself(ctx context.Context, params json.RawMessage) bool {
	_, boxed := callerBox(ctx)
	return !boxed && authz.FigaroOf(ctx) == "" && rpc.CallerOf(params) == "" && h.callerIsOwner(ctx)
}

func setThrough(ctx context.Context, set jkrpc.HandlerFunc, id string, req rpc.SetRequest) (rpc.SetResponse, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return rpc.SetResponse{}, err
	}
	if raw, err = rpc.WithAria(raw, id); err != nil {
		return rpc.SetResponse{}, err
	}
	out, err := set(ctx, raw)
	if err != nil {
		return rpc.SetResponse{}, err
	}
	switch r := out.(type) {
	case rpc.SetResponse:
		return r, nil
	case *rpc.SetResponse:
		return *r, nil
	}
	var resp rpc.SetResponse
	b, err := json.Marshal(out)
	if err == nil {
		err = json.Unmarshal(b, &resp)
	}
	if err != nil {
		return rpc.SetResponse{}, fmt.Errorf("set on %s answered %T: %w", id, out, err)
	}
	return resp, nil
}

func (h *handlers) drawnBelow(id string) []string {
	var out []string
	seen := map[string]bool{id: true}
	next := []string{id}
	for len(next) > 0 {
		at := next[0]
		next = next[1:]
		for _, kid := range h.angelus.topology().Children(at) {
			if !seen[kid] {
				seen[kid] = true
				out = append(out, kid)
				next = append(next, kid)
			}
		}
	}
	return out
}
