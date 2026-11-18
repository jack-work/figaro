package angelus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
)

// refuseCastPlacement keeps a held role from being handed to an aria the
// holder cannot reach: once a role has a holder, only that holder or a
// descendant of it in the drawn tree (the tree `ls` draws, which a
// reparent moves and a fork grows) may be cast into it. The owner acting
// with no figaro label is exempt; a labeled figaro is held to it whether
// or not a box can verify the label, the same honour C3 applies to
// fanout.
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
	if authz.FigaroOf(ctx) == "" && h.callerOwns(ctx) {
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
	if h.underInDrawnTree(id, *holder) {
		return nil
	}
	return fmt.Errorf("placement: %s is not under the holder %s of %s; fork the holder instead", id, *holder, roleID)
}

// underInDrawnTree walks id's drawn parents (not its fork lineage, which a
// reparent can leave behind) looking for holder, id included.
func (h *handlers) underInDrawnTree(id, holder string) bool {
	top := h.angelus.topology()
	cur := id
	seen := map[string]bool{}
	for cur != "" && !seen[cur] {
		if cur == holder {
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
