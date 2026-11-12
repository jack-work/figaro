package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/hierarchy"
)

func (h *handlers) parkedProposal(ctx context.Context, err error) (*rpc.Proposal, bool) {
	var parked *hierarchy.ParkedError
	if !errors.As(err, &parked) {
		return nil, false
	}
	p := h.shownProposal(parked.Proposal, h.readsIDs(ctx), h.consentsOn(ctx, parked.Proposal.Parent))
	return &p, true
}

func (h *handlers) readsIDs(ctx context.Context) func(id string) bool {
	sub, err := authz.SubjectOf(ctx)
	if err != nil {
		return func(id string) bool { return id == "" }
	}
	if h.angelus.Policies == nil {
		open := h.passesUnpoliced(ctx, sub)
		return func(id string) bool { return id == "" || open }
	}
	view := h.angelus.Policies.Figaro(authz.FigaroOf(ctx))
	return func(id string) bool {
		return id == "" || view.Allow(sub, rpc.VerbRead, authz.Aria(id)).Allowed
	}
}

func (h *handlers) consentsOn(ctx context.Context, parent string) bool {
	if h.allowed(ctx, rpc.VerbAdopt, authz.Aria(parent)) == nil {
		return true
	}
	by, err := h.angelus.topologyPrincipal(ctx)
	if err != nil || by.Name == "" {
		return false
	}
	meta, _ := h.angelus.Backend.Meta(parent)
	return meta != nil && meta.MintedBy == by.Name
}

func (h *handlers) shownProposal(p hierarchy.Proposal, readable func(string) bool, counts bool) rpc.Proposal {
	out := rpc.Proposal{
		ID:        p.ID,
		Parent:    p.Parent,
		Op:        p.Plan.Op.String(),
		By:        p.By.Name,
		Moves:     []rpc.ProposedMove{},
		Accepts:   p.Accepts,
		ExpiresMS: p.Expires.UnixMilli(),
	}
	if readable(p.Plan.Node) {
		out.Node = p.Plan.Node
	}
	for _, m := range p.Plan.Moves {
		if !readable(m.Node) {
			if counts {
				out.Withheld++
			}
			continue
		}
		shown := rpc.ProposedMove{Node: m.Node, Tree: m.Tree.String()}
		if readable(m.From) {
			shown.From = m.From
		} else {
			shown.FromWithheld = true
		}
		if readable(m.To) {
			shown.To = m.To
		} else {
			shown.ToWithheld = true
		}
		out.Moves = append(out.Moves, shown)
	}
	return out
}

func (h *handlers) parked(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ParkedRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	by, err := h.angelus.topologyPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	adopter := h.allowed(ctx, rpc.VerbAdopt, authz.Aria(req.FigaroID)) == nil
	counts := h.consentsOn(ctx, req.FigaroID)
	readable := h.readsIDs(ctx)
	resp := rpc.ParkedResponse{Proposals: []rpc.Proposal{}}
	for _, p := range h.angelus.topology().Parked(req.FigaroID) {
		if !adopter && p.By.Name != by.Name {
			continue
		}
		resp.Proposals = append(resp.Proposals, h.shownProposal(p, readable, counts))
	}
	return resp, nil
}

func (h *handlers) accept(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ConsentRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	by, err := h.angelus.topologyPrincipal(ctx)
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
	by, err := h.angelus.topologyPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.angelus.topology().Refuse(ctx, by, req.FigaroID, req.ID); err != nil {
		return nil, fmt.Errorf("refuse %s on %s: %w", req.ID, req.FigaroID, err)
	}
	slog.Info("refused a parked move", "parent", req.FigaroID, "proposal", req.ID, "by", by.Name)
	return rpc.ConsentResponse{OK: true}, nil
}
