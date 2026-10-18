package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/jkrpc"
)

const (
	minIDPrefix   = 4
	ambiguityList = 5
)

var secondaryIDKeys = []string{"form_id", "role_id", "against"}

func (h *handlers) resolvingIDs(method string, next jkrpc.HandlerFunc) jkrpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		params, node, err := h.placeParams(ctx, method, params)
		if err != nil {
			return nil, err
		}
		if node != "" {
			return h.forward(ctx, node, method, params)
		}
		return next(ctx, params)
	}
}

func (h *handlers) placeParams(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, string, error) {
	if ref := rpc.AriaOf(params); ref != "" {
		id, node, err := h.placeID(ctx, ref, h.routes(ctx, method))
		if errors.Is(err, peer.ErrNoOwner) {
			return nil, "", authz.NotFound(method, ref)
		}
		if err != nil {
			return nil, "", err
		}
		if id != ref {
			if params, err = rpc.WithAria(params, id); err != nil {
				return nil, "", err
			}
		}
		if node != "" {
			return params, node, nil
		}
	}
	params, err := h.canonicalParams(ctx, params)
	return params, "", err
}

func (h *handlers) canonicalParams(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
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
	id, _, err := h.placeID(ctx, ref, false)
	return id, err
}

func (h *handlers) placeID(ctx context.Context, ref string, route bool) (string, string, error) {
	node, id := rpc.SplitNode(ref)
	if node != "" && !h.isSelf(node) {
		return h.elsewhere(ctx, ref, node, id, route)
	}
	if err := h.holdsMove(id); err != nil {
		return "", "", err
	}
	if h.knownID(id) {
		return id, "", nil
	}
	if t, ok := h.tombstone(id); ok {
		return h.towardsMoved(ctx, t, route)
	}
	if w, ok := handle.Parse(id); ok && h.angelus.Backend != nil {
		return h.placeWords(ctx, id, w, route)
	}
	if handle.IsPrefix(id) && !rpc.IsHexID(id) && h.angelus.Backend != nil {
		return h.placeWordPrefix(ctx, id)
	}
	if len(strings.TrimPrefix(id, "@")) < minIDPrefix || h.angelus.Backend == nil {
		return id, "", nil
	}
	matches := h.matchReadable(ctx, id)
	switch len(matches) {
	case 0:
		if t, ok := h.tombstoneByPrefix(ctx, id); ok {
			return h.towardsMoved(ctx, t, route)
		}
		if handle.IsPrefix(id) {
			if w, node, err := h.placeWordPrefix(ctx, id); err != nil || w != id {
				return w, node, err
			}
		}
		if route {
			return h.locate(ctx, id)
		}
		return id, "", nil
	case 1:
		return matches[0], "", nil
	}
	more := ""
	if len(matches) > ambiguityList {
		matches, more = matches[:ambiguityList], ", ..."
	}
	return "", "", fmt.Errorf("%s is ambiguous: %s%s", id, strings.Join(matches, ", "), more)
}

func (h *handlers) placeWords(ctx context.Context, ref string, w handle.Ref, route bool) (string, string, error) {
	var matches []string
	for _, id := range h.matchReadable(ctx, w.HexPrefix()) {
		if w.Matches(id) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		if route && w.N == handle.MinWords {
			return h.locate(ctx, w.HexPrefix())
		}
		return ref, "", nil
	case 1:
		return matches[0], "", nil
	}
	named := make([]string, len(matches))
	for i, id := range matches {
		named[i] = handle.Words(id, handle.MaxWords)
	}
	return "", "", fmt.Errorf("%s is ambiguous: %s", ref, strings.Join(named, ", "))
}

func (h *handlers) placeWordPrefix(ctx context.Context, ref string) (string, string, error) {
	sigil := ref[:len(ref)-len(strings.TrimPrefix(ref, "@"))]
	var matches []string
	for _, id := range h.angelus.Backend.MatchID(sigil, -1) {
		if !strings.HasPrefix(handle.Words(id, handle.MaxWords), ref) {
			continue
		}
		if h.angelus.Policies != nil && !h.readableID(ctx, id) {
			continue
		}
		matches = append(matches, id)
		if len(matches) > ambiguityList {
			break
		}
	}
	switch len(matches) {
	case 0:
		return ref, "", nil
	case 1:
		return matches[0], "", nil
	}
	named := make([]string, len(matches))
	for i, id := range matches {
		named[i] = handle.Words(id, handle.MaxWords)
	}
	return "", "", fmt.Errorf("%s is ambiguous: %s", ref, strings.Join(named, ", "))
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

func (h *handlers) readableID(ctx context.Context, id string) bool {
	sub, err := authz.SubjectOf(ctx)
	if err != nil {
		return false
	}
	return h.angelus.Policies.Allow(sub, rpc.VerbRead, authz.Aria(id)).Allowed
}
