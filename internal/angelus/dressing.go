package angelus

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jack-work/figaro/internal/outfit"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

// dress is the only place in the daemon that reads an outfit file on a
// request's behalf. `default` is the one lenient name; everything else is strict.
func (h *handlers) dress(outfits []string, patch *rpc.FormPatch) (form.Patch, error) {
	var base form.Patch
	if patch != nil {
		base = *patch
	}
	if len(outfits) == 0 {
		return base, nil
	}
	loaded, ofit := h.settings()
	if ofit == nil {
		return form.Patch{}, fmt.Errorf("outfit: %s: no outfit directory configured", strings.Join(outfits, ","))
	}
	def := ""
	if loaded != nil {
		def = loaded.Config.DefaultOutfit
	}
	out, err := ofit.Dress(outfits, base, def)
	var missing *outfit.MissingError
	if errors.As(err, &missing) {
		return form.Patch{}, h.errOutfitNotFound(strings.Join(outfits, ","), err)
	}
	if err != nil {
		return form.Patch{}, err
	}
	return out, nil
}

// dressDefault folds the default outfit alone: folding a caller's -O into
// it would mint a private form per literal and cost the shared prompt prefix.
func (h *handlers) dressDefault() (form.Patch, error) {
	return h.dress([]string{outfitNameDefault}, nil)
}

const outfitNameDefault = "default"

// dressParams expands outfit names before a request enters the hub; a
// request naming none is handed back byte for byte.
func (h *handlers) dressParams(method string, params json.RawMessage) (json.RawMessage, error) {
	if len(params) == 0 || !mightCarryOutfits(params) {
		return params, nil
	}
	switch method {
	case rpc.MethodSet:
		var req rpc.SetRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return params, err
		}
		if len(req.Outfits) == 0 {
			return params, nil
		}
		dressed, err := h.dress(req.Outfits, &req.Patch)
		if err != nil {
			return params, err
		}
		req.Patch, req.Outfits = dressed, nil
		return json.Marshal(req)

	case rpc.MethodQua:
		var req rpc.QuaRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return params, err
		}
		if req.Form == nil || len(req.Form.Outfits) == 0 {
			return params, nil
		}
		dressed, err := h.dress(req.Form.Outfits, req.Form.Patch)
		if err != nil {
			return params, err
		}
		req.Form.Patch, req.Form.Outfits = &dressed, nil
		return remarshalWithSender(params, req)

	case rpc.MethodCast:
		var req rpc.CastRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return params, err
		}
		if len(req.Outfits) == 0 {
			return params, nil
		}
		dressed, err := h.dress(req.Outfits, req.RolePatch)
		if err != nil {
			return params, err
		}
		req.RolePatch, req.Outfits = &dressed, nil
		return json.Marshal(req)
	}
	return params, nil
}

func mightCarryOutfits(params json.RawMessage) bool {
	return strings.Contains(string(params), `"outfits"`)
}

// remarshalWithSender keeps sibling keys the typed struct does not model;
// `sender` is read off the raw params, and dropping it makes the prompt anonymous.
func remarshalWithSender(orig json.RawMessage, req any) (json.RawMessage, error) {
	next, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var was, now map[string]json.RawMessage
	if err := json.Unmarshal(orig, &was); err != nil {
		return next, nil
	}
	if err := json.Unmarshal(next, &now); err != nil {
		return next, nil
	}
	for k, v := range was {
		if _, taken := now[k]; !taken {
			now[k] = v
		}
	}
	return json.Marshal(now)
}
