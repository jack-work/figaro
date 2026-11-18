package angelus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

func (h *handlers) refuseCastPlacement(ctx context.Context, method, id string, params json.RawMessage) error {
	if method != rpc.MethodCast {
		return nil
	}
	var req rpc.CastRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil
	}
	roleID := req.FormID
	if roleID == "" {
		return nil
	}
	if authz.FigaroOf(ctx) == "" && !authz.Claimed(ctx) && h.callerOwns(ctx) {
		return nil
	}
	snap, err := h.angelus.Backend.FormState(roleID)
	if err != nil {
		return nil
	}
	holder := snap.Lookup("target-aria")
	if holder == nil || *holder == "" || *holder == id {
		return nil
	}
	if h.mayTakeRole(id, *holder) {
		return nil
	}
	return fmt.Errorf("placement: %s is neither under the holder %s of %s nor a fork of it demoted over it; fork the holder instead", id, *holder, roleID)
}

func (h *handlers) mayTakeRole(candidate, holder string) bool {
	if h.ancestorInDrawnTree(candidate, holder) {
		return true
	}
	parent, ok := h.angelus.topology().Drawn(holder)
	return ok && parent == candidate && h.angelus.forkDescends(candidate, holder)
}

func (h *handlers) ancestorInDrawnTree(id, ancestor string) bool {
	top := h.angelus.topology()
	cur := id
	seen := map[string]bool{}
	for cur != "" && !seen[cur] {
		if cur == ancestor {
			return true
		}
		seen[cur] = true
		parent, ok := top.Drawn(cur)
		if !ok {
			return false
		}
		cur = parent
	}
	return false
}
