package angelus

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/rpc"
)

// The API boundary's ONE materialization point.

// dress folds the named outfits and lands the caller's patch on top. It is the
// only place in the daemon that reads an outfit file on a request's behalf.
// The reserved name `default` resolves to whatever config calls the default
// outfit and is the one lenient name; everything else is strict.
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
	if err != nil {
		return form.Patch{}, h.errOutfitNotFound(strings.Join(outfits, ","), err)
	}
	return out, nil
}

// dressDefault is the birth fold every `fig new` rides: the configured default
// outfit alone, materialized. It is separate from dress because the default
// form's identity is a pure function of that closure: folding a caller's -O
// into it would mint a private form per literal and cost the shared prompt
// prefix (the lesson of 0.22.1, recorded at create).
func (h *handlers) dressDefault() (form.Patch, error) {
	return h.dress([]string{outfitNameDefault}, nil)
}

const outfitNameDefault = "default"

// dressParams is the same call, applied to a request on its way INTO an aria's
// hub: figaro.set, figaro.qua and figaro.cast all accept outfit names, and
// all three must arrive at the agent (or at the hub's agentless writer) with
// nothing left to expand. It rewrites the params in place and returns them;
// a request naming no outfit is handed back untouched, byte for byte, so the
// common path costs one unmarshal-free comparison.
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
		// The sender rides in the params envelope beside the request's own
		// fields (rpc.SenderFrom reads it off the raw bytes), so it is
		// carried across the rewrite rather than dropped.
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

// mightCarryOutfits is the cheap negative: no `outfits` key in the bytes, no
// work. Every read path and every plain `set` takes this exit.
func mightCarryOutfits(params json.RawMessage) bool {
	return strings.Contains(string(params), `"outfits"`)
}

// remarshalWithSender rebuilds a params object from a typed request while
// preserving the sibling keys the typed struct does not model: today the
// attribution envelope (`sender`), which an agent reads off the raw params.
// Dropping it would make every dressed prompt anonymous.
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
