package angelus

// Attaching an aria, and the pid bindings a shell holds.
//
// Split out of protocol.go, which had grown to 2,011 lines and answered every
// question at once. Same package, same behaviour: only the reader's job
// changes. plans/api-coherence.md step 5.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
)

// bind attends a shell to an aria WITHOUT waking it. Binding is an identity
// fact, so the only thing that must exist is the aria on disk and an endpoint
// to dial: never an agent. This is what makes `figaro attend` free.
func (h *handlers) bind(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.BindRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := h.requireAria(req.FigaroID); err != nil {
		return nil, fmt.Errorf("bind: %w", err)
	}
	// The endpoint must be listening before the caller is told the bind
	// succeeded: a unix socket cannot be lazily activated, so a client that
	// dials an unopened path gets ECONNREFUSED rather than a wakeup.
	if _, err := h.hubFor(req.FigaroID); err != nil {
		return nil, fmt.Errorf("bind: %w", err)
	}
	if err := h.angelus.Registry.Bind(req.PID, req.FigaroID, req.AtMainLT); err != nil {
		return nil, err
	}
	return rpc.BindResponse{OK: true}, nil
}

// requireAria proves an aria exists without constructing anything. A bad id
// must still be an error, attending a typo has to fail, not open a socket
// for a conversation that was never born.
func (h *handlers) requireAria(id string) error {
	if err := rpc.ValidateAriaID(id); err != nil {
		return err
	}
	if h.angelus.Registry.Get(id) != nil {
		return nil // live: already proven
	}
	// A topology lookup, not a read: Meta is NOT an existence check (it
	// returns nil,nil for an unknown aria, and arias predating the sidecar
	// legitimately have none: see backfill.go), and Open would decode the
	// whole IR just to prove the aria is there.
	if _, ok := h.angelus.Backend.Node(id); !ok {
		return fmt.Errorf("aria %s not found", id)
	}
	return nil
}

// attach opens an aria's endpoint without touching pid bindings and WITHOUT
// waking it. It used to restore; it now only guarantees somewhere to dial.
// The aria wakes on the first request that needs a turn loop, which for a
// transcript or a pager is never.
func (h *handlers) attach(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.AttachRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := h.requireAria(req.FigaroID); err != nil {
		return nil, fmt.Errorf("attach %s: %w", req.FigaroID, err)
	}
	hb, err := h.hubFor(req.FigaroID)
	if err != nil {
		return nil, fmt.Errorf("attach %s: %w", req.FigaroID, err)
	}
	return rpc.AttachResponse{
		FigaroID: req.FigaroID,
		Endpoint: rpc.Endpoint{Scheme: "unix", Address: hb.sockPath},
	}, nil
}

func (h *handlers) resolve(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ResolveRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	// A dormant aria is a FOUND aria. The binding is the answer and the
	// address is a pure function of the id, so nothing here needs an agent -
	// resolving must not be the thing that wakes what the sweep reclaimed.
	id, _, lt := h.angelus.Registry.Resolve(req.PID)
	if id == "" {
		return rpc.ResolveResponse{Found: false}, nil
	}
	hb, err := h.hubFor(id)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", id, err)
	}
	return rpc.ResolveResponse{
		FigaroID: id,
		Endpoint: rpc.Endpoint{Scheme: "unix", Address: hb.sockPath},
		Found:    true,
		AtMainLT: lt,
	}, nil
}

func (h *handlers) unbind(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.UnbindRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	h.angelus.Registry.Unbind(req.PID)
	return rpc.UnbindResponse{OK: true}, nil
}
