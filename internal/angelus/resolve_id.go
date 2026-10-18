package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/jkrpc"
)

const (
	minIDPrefix   = 4
	ambiguityList = 5
)

var secondaryIDKeys = []string{"form_id", "role_id", "against"}

func (h *handlers) resolvingIDs(next jkrpc.HandlerFunc) jkrpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		params, err := h.canonicalParams(ctx, params)
		if err != nil {
			return nil, err
		}
		return next(ctx, params)
	}
}

func (h *handlers) canonicalParams(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	if ref := rpc.AriaOf(params); ref != "" {
		id, err := h.canonicalID(ctx, ref)
		if err != nil {
			return nil, err
		}
		if id != ref {
			if params, err = rpc.WithAria(params, id); err != nil {
				return nil, err
			}
		}
	}
	if !mayCarrySecondaryID(params) {
		return params, nil
	}
	fields := map[string]json.RawMessage{}
	if json.Unmarshal(params, &fields) != nil {
		return params, nil
	}
	changed := false
	for _, k := range secondaryIDKeys {
		var ref string
		if raw, ok := fields[k]; !ok || json.Unmarshal(raw, &ref) != nil || ref == "" {
			continue
		}
		id, err := h.canonicalID(ctx, ref)
		if err != nil {
			return nil, err
		}
		if id != ref {
			fields[k], _ = json.Marshal(id)
			changed = true
		}
	}
	if !changed {
		return params, nil
	}
	return json.Marshal(fields)
}

func mayCarrySecondaryID(params json.RawMessage) bool {
	for _, k := range secondaryIDKeys {
		if bytes.Contains(params, []byte(`"`+k+`"`)) {
			return true
		}
	}
	return false
}

func (h *handlers) canonicalID(ctx context.Context, ref string) (string, error) {
	node, id := rpc.SplitNode(ref)
	if node != "" {
		own := h.nodeID()
		if own == "" || !(node == own || len(node) >= rpc.ShortIDLen && strings.HasPrefix(own, node)) {
			return "", fmt.Errorf("%s is on node %s, and this is node %s: reaching another node arrives with peering", ref, node, own)
		}
	}
	if h.knownID(id) {
		return id, nil
	}
	if len(strings.TrimPrefix(id, "@")) < minIDPrefix || h.angelus.Backend == nil {
		return id, nil
	}
	matches := h.matchReadable(ctx, id)
	switch len(matches) {
	case 0:
		return id, nil
	case 1:
		return matches[0], nil
	}
	more := ""
	if len(matches) > ambiguityList {
		matches, more = matches[:ambiguityList], ", ..."
	}
	return "", fmt.Errorf("%s is ambiguous: %s%s", id, strings.Join(matches, ", "), more)
}

func (h *handlers) matchReadable(ctx context.Context, prefix string) []string {
	if h.angelus.Policies == nil {
		return h.angelus.Backend.MatchID(prefix, ambiguityList+1)
	}
	sub, err := authz.SubjectOf(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, id := range h.angelus.Backend.MatchID(prefix, -1) {
		if h.angelus.Policies.Allow(sub, rpc.VerbRead, authz.Aria(id)).Allowed {
			out = append(out, id)
			if len(out) > ambiguityList {
				break
			}
		}
	}
	return out
}

func (h *handlers) knownID(id string) bool {
	if id == models.FormID {
		return true
	}
	if h.angelus.Hubs != nil && h.angelus.Hubs.get(id) != nil {
		return true
	}
	if h.angelus.Registry != nil && h.angelus.Registry.Get(id) != nil {
		return true
	}
	if h.angelus.Backend == nil {
		return false
	}
	_, ok := h.angelus.Backend.Node(id)
	return ok
}
