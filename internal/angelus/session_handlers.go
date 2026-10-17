package angelus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
)

func (h *handlers) bind(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.BindRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := h.requireAria(req.FigaroID); err != nil {
		return nil, fmt.Errorf("bind: %w", err)
	}
	if err := h.angelus.Registry.Bind(req.PID, req.FigaroID, req.AtMainLT); err != nil {
		return nil, err
	}
	return rpc.BindResponse{OK: true}, nil
}

// requireAria proves an aria exists without constructing anything.
func (h *handlers) requireAria(id string) error {
	if err := rpc.ValidateAriaID(id); err != nil {
		return err
	}
	if id == models.FormID && h.angelus.Models != nil {
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
	return rpc.AttachResponse{FigaroID: req.FigaroID}, nil
}

func (h *handlers) resolve(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ResolveRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	// A dormant aria is a found aria; resolving must not wake it.
	id, _, lt := h.angelus.Registry.Resolve(req.PID)
	if id == "" {
		return rpc.ResolveResponse{Found: false}, nil
	}
	return rpc.ResolveResponse{FigaroID: id, Found: true, AtMainLT: lt}, nil
}

func (h *handlers) unbind(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.UnbindRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	h.angelus.Registry.Unbind(req.PID)
	return rpc.UnbindResponse{OK: true}, nil
}
