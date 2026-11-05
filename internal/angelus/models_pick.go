package angelus

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
)

func (h *handlers) modelsPick(ctx context.Context, params json.RawMessage) (any, error) {
	var req rpc.ModelsPickRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	if err := rpc.ValidateAriaID(req.FigaroID); err != nil {
		return nil, err
	}
	f := h.catalog(models.FormID)
	if f == nil {
		return nil, fmt.Errorf("models pick: this daemon keeps no models catalog")
	}
	snap, _ := f.Snapshot()
	e, ok := models.Lookup(snap, req.Provider, req.Model)
	if !ok || e.Hidden {
		return nil, fmt.Errorf("models pick: %s has no listed model %q", req.Provider, req.Model)
	}
	set := map[string]json.RawMessage{}
	for k, v := range map[string]string{models.ProviderKey: req.Provider, models.ModelKey: req.Model} {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		set[k] = raw
	}
	raw, err := json.Marshal(rpc.SetRequest{Patch: form.Build(form.Snapshot{}, set, nil)})
	if err != nil {
		return nil, err
	}
	withAria, err := rpc.WithAria(raw, req.FigaroID)
	if err != nil {
		return nil, err
	}
	if _, err := h.ariaMethod(rpc.MethodSet)(ctx, withAria); err != nil {
		return nil, fmt.Errorf("models pick: %w", err)
	}
	return rpc.ModelsPickResponse{Provider: req.Provider, Model: req.Model}, nil
}
