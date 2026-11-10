package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/jkrpc"
)

const (
	minIDPrefix   = 4
	ambiguityList = 5
	fullIDLen     = 32
)

var secondaryIDKeys = []string{"form_id", "role_id", "against", "prefer"}

func (h *handlers) resolvingIDs(method string, next jkrpc.HandlerFunc) jkrpc.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		params, node, ref, err := h.placeParams(ctx, method, params)
		if err != nil {
			return nil, err
		}
		if node == "" {
			return next(ctx, params)
		}
		res, err := h.forward(ctx, node, method, params)
		var answered *jkrpc.Error
		if err != nil && !errors.As(err, &answered) && !h.mayLearnFate(ctx, method, rpc.AriaOf(params)) {
			return nil, authz.NotFound(method, ref)
		}
		return res, err
	}
}

func (h *handlers) placeParams(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, string, string, error) {
	ref := rpc.AriaOf(params)
	if ref != "" {
		id, node, err := h.placeID(ctx, method, ref, h.routes(ctx, method))
		if errors.Is(err, peer.ErrNoOwner) {
			return nil, "", ref, authz.NotFound(method, ref)
		}
		if err != nil {
			return nil, "", ref, err
		}
		if id != ref {
			if params, err = rpc.WithAria(params, id); err != nil {
				return nil, "", ref, err
			}
		}
		if node != "" {
			return params, node, ref, nil
		}
	}
	params, err := h.canonicalParams(ctx, params)
	return params, "", ref, err
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
		if _, named := roleNameOf(ref); named && id == ref && !h.knownID(id) {
			return nil, fmt.Errorf("no role or form named %s", ref)
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
	id, _, err := h.placeID(ctx, "", ref, false)
	return id, err
}

func (h *handlers) mayLearnFate(ctx context.Context, method, id string) bool {
	if h.allowed(ctx, rpc.VerbRead, authz.Aria(id)) == nil {
		return true
	}
	verbs, _ := rpc.VerbsOf(method)
	for _, verb := range verbs {
		if verb != rpc.VerbNone && verb != rpc.VerbRead && h.allowed(ctx, verb, authz.Aria(id)) == nil {
			return true
		}
	}
	return false
}

func (h *handlers) placeID(ctx context.Context, method, ref string, route bool) (string, string, error) {
	name, named := roleNameOf(ref)
	if !named {
		return h.placeRef(ctx, method, ref, route)
	}
	id, node, err := h.placeRef(ctx, method, ref, false)
	if err != nil || id != ref || h.knownID(id) {
		return id, node, err
	}
	if named, found, err := h.formNamed(ctx, name); found || err != nil {
		return named, "", err
	}
	if !route {
		return id, node, nil
	}
	return h.placeRef(ctx, method, ref, route)
}

func roleNameOf(ref string) (string, bool) {
	name, ok := strings.CutPrefix(ref, "@")
	return name, ok && name != "" && !(len(name) == fullIDLen && rpc.IsHexID(name)) && !strings.Contains(ref, rpc.NodeSeparator)
}

type formNamer interface {
	FormsNamed(name string) (roles, forms []string, ready bool)
}

func (h *handlers) formNamed(ctx context.Context, name string) (string, bool, error) {
	roles, forms := h.formsNamed(name)
	box, _ := callerBox(ctx)
	roles, forms = h.nameableFrom(box, roles), h.nameableFrom(box, forms)
	if id, found, err := h.oneReadable(ctx, name, "roles", roles); found || err != nil {
		return id, found, err
	}
	return h.oneReadable(ctx, name, "forms", forms)
}

func (h *handlers) nameableFrom(box string, ids []string) []string {
	var out []string
	for _, id := range ids {
		snap, err := h.angelus.Backend.FormState(id)
		if err != nil {
			continue
		}
		if held := h.heldBox(snap); held == "" || held == box {
			out = append(out, id)
		}
	}
	return out
}

func (h *handlers) formsNamed(name string) (roles, forms []string) {
	if n, ok := h.angelus.Backend.(formNamer); ok {
		roles, forms, ready := n.FormsNamed(name)
		if ready {
			return roles, forms
		}
	}
	if h.angelus.Backend == nil {
		return nil, nil
	}
	return h.scanFormsNamed(name)
}

func (h *handlers) oneReadable(ctx context.Context, name, species string, ids []string) (string, bool, error) {
	var readable []string
	for _, id := range ids {
		if h.allowed(ctx, rpc.VerbRead, authz.Aria(id)) == nil {
			readable = append(readable, id)
		}
	}
	switch len(readable) {
	case 0:
		return "", false, nil
	case 1:
		return readable[0], true, nil
	}
	named := make([]string, len(readable))
	for i, id := range readable {
		named[i] = handle.Words(id, handle.MinWords)
	}
	return "", false, fmt.Errorf("@%s names %d %s: %s; name one by its handle", name, len(readable), species, strings.Join(named, ", "))
}

func (h *handlers) scanFormsNamed(name string) (roles, forms []string) {
	for _, f := range h.angelus.Backend.Forms() {
		snap, err := h.angelus.Backend.FormState(f.ID)
		if err != nil {
			continue
		}
		own := snap.Lookup("name")
		if own == nil || *own != name {
			continue
		}
		if target := snap.Lookup("target-aria"); target != nil && *target != "" {
			roles = append(roles, f.ID)
		} else {
			forms = append(forms, f.ID)
		}
	}
	return roles, forms
}

func (h *handlers) placeRef(ctx context.Context, method, ref string, route bool) (string, string, error) {
	node, id := rpc.SplitNode(ref)
	if node != "" && !h.isSelf(node) && method == rpc.MethodBind {
		qualified, err := h.memberRef(ref)
		return qualified, "", err
	}
	if node != "" && !h.isSelf(node) {
		return h.elsewhere(ctx, method, ref, node, id, route)
	}
	if err := h.holdsMove(id); err != nil && h.mayLearnFate(ctx, method, id) {
		return "", "", err
	}
	if h.knownID(id) {
		return id, "", nil
	}
	if t, ok := h.tombstone(id); ok && h.mayLearnFate(ctx, method, id) {
		return h.towardsMoved(ctx, method, t, route)
	}
	if w, ok := handle.Parse(id); ok && h.angelus.Backend != nil {
		return h.placeWords(ctx, method, id, w, route)
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
			return h.towardsMoved(ctx, method, t, route)
		}
		if handle.IsPrefix(id) {
			if w, node, err := h.placeWordPrefix(ctx, id); err != nil || w != id {
				return w, node, err
			}
		}
		if route {
			return h.locate(ctx, method, id)
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

func (h *handlers) placeWords(ctx context.Context, method, ref string, w handle.Ref, route bool) (string, string, error) {
	var matches []string
	for _, id := range h.matchReadable(ctx, w.HexPrefix()) {
		if w.Matches(id) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		if route && w.N == handle.MinWords {
			return h.locate(ctx, method, w.HexPrefix())
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
	sub, err := authz.SubjectOf(ctx)
	if err != nil {
		return nil
	}
	if h.angelus.Policies == nil {
		if h.passesUnpoliced(ctx, sub) {
			return h.angelus.Backend.MatchID(prefix, ambiguityList+1)
		}
		return nil
	}
	view := h.angelus.Policies.Figaro(authz.FigaroOf(ctx))
	var out []string
	for _, id := range h.angelus.Backend.MatchID(prefix, -1) {
		if view.Allow(sub, rpc.VerbRead, authz.Aria(id)).Allowed {
			out = append(out, id)
			if len(out) > ambiguityList {
				break
			}
		}
	}
	return out
}

func (h *handlers) knownID(id string) bool {
	if isLocalFormID(id) {
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
	return h.angelus.Policies.AllowFigaro(sub, authz.FigaroOf(ctx), rpc.VerbRead, authz.Aria(id)).Allowed
}
