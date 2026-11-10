package angelus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

func (h *handlers) bind(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.BindRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := h.requireAria(req.FigaroID); err != nil {
		return nil, fmt.Errorf("bind: %w", err)
	}
	done, err := h.angelus.guard.enter(req.FigaroID)
	if err != nil {
		return nil, fmt.Errorf("bind: %w", err)
	}
	defer done()
	if err := h.angelus.Registry.Bind(req.PID, req.FigaroID, req.AtMainLT); err != nil {
		return nil, err
	}
	h.navPush(req.PID, req.FigaroID)
	return rpc.BindResponse{OK: true}, nil
}

func (h *handlers) memberRef(ref string) (string, error) {
	if err := rpc.ValidateAriaRef(ref); err != nil {
		return "", err
	}
	node, id := rpc.SplitNode(ref)
	full, ok := h.peerNamed(node)
	if !ok || h.isSelf(full) {
		return "", fmt.Errorf("%s: node %s is not a member of this node's troupe", ref, node)
	}
	return full + rpc.NodeSeparator + id, nil
}

// requireAria proves an aria exists without constructing anything.
func (h *handlers) requireAria(id string) error {
	if node, _ := rpc.SplitNode(id); node != "" && !h.isSelf(node) {
		_, err := h.memberRef(id)
		return err
	}
	if err := rpc.ValidateAriaID(id); err != nil {
		return err
	}
	if h.catalog(id) != nil || isCallerFormID(id) || isNavFormID(id) {
		return nil
	}
	if h.angelus.Registry.Get(id) != nil {
		return nil
	}
	// Meta is not an existence check (nil,nil for unknown), and Open would decode the IR.
	if _, ok := h.angelus.Backend.Node(id); !ok {
		return fmt.Errorf("aria %s not found", id)
	}
	return nil
}

func (h *handlers) attach(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.AttachRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := h.requireAria(req.FigaroID); err != nil {
		return nil, fmt.Errorf("attach %s: %w", req.FigaroID, err)
	}
	return rpc.AttachResponse{FigaroID: req.FigaroID, May: h.mayOn(ctx, req.FigaroID)}, nil
}

func (h *handlers) mayOn(ctx context.Context, id string) []rpc.Verb {
	may := []rpc.Verb{}
	for _, v := range []rpc.Verb{rpc.VerbRead, rpc.VerbListen, rpc.VerbSend} {
		if h.allowed(ctx, v, authz.Aria(id)) == nil {
			may = append(may, v)
		}
	}
	return may
}

func (h *handlers) resolve(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ResolveRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	// A dormant aria is a found aria; resolving must not wake it.
	id, _, lt := h.angelus.Registry.Resolve(req.PID)
	hist := h.nav.Get(req.PID)
	if id == "" {
		return rpc.ResolveResponse{Found: false, History: hist.IDs, Pos: hist.Pos}, nil
	}
	return rpc.ResolveResponse{FigaroID: id, Found: true, AtMainLT: lt, History: hist.IDs, Pos: hist.Pos}, nil
}

func (h *handlers) unbind(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.UnbindRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	h.angelus.Registry.Unbind(req.PID)
	return rpc.UnbindResponse{OK: true}, nil
}
