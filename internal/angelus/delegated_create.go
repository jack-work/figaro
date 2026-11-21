package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
)

func delegatedVia(ctx context.Context) (string, bool) {
	sub, err := authz.SubjectOf(ctx)
	if err != nil {
		return "", false
	}
	via, ok := authz.DelegatedVia(sub)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s acts for %s", via, sub.Principal), true
}

func (h *handlers) shadowKeyGrants(ctx context.Context, err error) error {
	if _, delegated := delegatedVia(ctx); delegated {
		return err
	}
	return h.angelus.Policies.Shadow(authz.KeyGrants, err)
}

func (h *handlers) enforcesKeyGrantsFor(ctx context.Context) bool {
	if _, delegated := delegatedVia(ctx); delegated {
		return true
	}
	return h.angelus.Policies.Enforces(authz.KeyGrants)
}

func (h *handlers) refuseUndeclaredOutfits(ctx context.Context, outfits []string) error {
	who, ok := delegatedVia(ctx)
	if !ok || len(outfitLayers(outfits)) == 0 {
		return nil
	}
	loaded, _ := h.settings()
	if loaded == nil {
		return fmt.Errorf("%s and may name declared outfits only, and this daemon has no outfit list", who)
	}
	idx, err := loaded.OutfitIndex()
	if err != nil {
		return fmt.Errorf("outfits: %w", err)
	}
	declared := idx.Names()
	for _, name := range outfitLayers(outfits) {
		if !slices.Contains(declared, name) {
			return fmt.Errorf("%s and may name declared outfits only: %q is not one (angelus.outfits lists them)", who, name)
		}
	}
	return nil
}

func (h *handlers) createdFor(ctx context.Context) (store.BirthOption, error) {
	who, ok := delegatedVia(ctx)
	if !ok {
		return mintedBy(ctx), nil
	}
	owner := h.angelus.owner()
	if owner.IsZero() {
		return nil, fmt.Errorf("mint: %s, and a delegated figaro is born the owner's, but this daemon knows no owner", who)
	}
	return store.MintedBy(owner.String()), nil
}

type judgedVersion struct{}

func withJudgedVersion(ctx context.Context) (context.Context, *uint64) {
	v := new(uint64)
	return context.WithValue(ctx, judgedVersion{}, v), v
}

func pinSetToJudgedVersion(ctx context.Context, method string, params json.RawMessage, version uint64) (json.RawMessage, error) {
	if _, delegated := delegatedVia(ctx); !delegated || method != rpc.MethodSet || version == 0 {
		return params, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, err
	}
	fields["if_version"] = json.RawMessage(strconv.FormatUint(version, 10))
	delete(fields, "seq")
	return json.Marshal(fields)
}

func (h *handlers) refuseForkOfAMovedBoard(ctx context.Context, parent string, judged uint64) error {
	if _, delegated := delegatedVia(ctx); !delegated {
		return nil
	}
	now, err := h.angelus.Backend.FormAt(parent)
	if err != nil {
		return err
	}
	if now.Version != judged {
		return fmt.Errorf("fork: %w: %s was at version %d when the patch was judged and is at %d, retry", store.ErrFormMoved, parent, judged, now.Version)
	}
	return nil
}
