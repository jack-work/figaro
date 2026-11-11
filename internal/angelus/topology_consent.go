package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/hierarchy"
)

func parkedProposal(err error) (*rpc.Proposal, bool) {
	var parked *hierarchy.ParkedError
	if !errors.As(err, &parked) {
		return nil, false
	}
	p := wireProposal(parked.Proposal)
	return &p, true
}

func wireProposal(p hierarchy.Proposal) rpc.Proposal {
	out := rpc.Proposal{
		ID:        p.ID,
		Parent:    p.Parent,
		Op:        p.Plan.Op.String(),
		Node:      p.Plan.Node,
		By:        p.By.Name,
		Moves:     []rpc.ProposedMove{},
		Accepts:   p.Accepts,
		ExpiresMS: p.Expires.UnixMilli(),
	}
	for _, m := range p.Plan.Moves {
		out.Moves = append(out.Moves, rpc.ProposedMove{Node: m.Node, From: m.From, To: m.To, Tree: m.Tree.String()})
	}
	return out
}

func (h *handlers) parked(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ParkedRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	resp := rpc.ParkedResponse{Proposals: []rpc.Proposal{}}
	for _, p := range h.angelus.topology().Parked(req.FigaroID) {
		resp.Proposals = append(resp.Proposals, wireProposal(p))
	}
	return resp, nil
}

func (h *handlers) accept(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ConsentRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	by, err := topologyPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := h.angelus.topology().Accept(ctx, by, req.FigaroID, req.ID); err != nil {
		return nil, fmt.Errorf("accept %s on %s: %w", req.ID, req.FigaroID, err)
	}
	slog.Info("accepted a parked move", "parent", req.FigaroID, "proposal", req.ID, "by", by.Name)
	return rpc.ConsentResponse{OK: true}, nil
}

func (h *handlers) refuse(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ConsentRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	by, err := topologyPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.angelus.topology().Refuse(ctx, by, req.FigaroID, req.ID); err != nil {
		return nil, fmt.Errorf("refuse %s on %s: %w", req.ID, req.FigaroID, err)
	}
	slog.Info("refused a parked move", "parent", req.FigaroID, "proposal", req.ID, "by", by.Name)
	return rpc.ConsentResponse{OK: true}, nil
}
