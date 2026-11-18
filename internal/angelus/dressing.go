package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/outfit"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
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
	start := time.Now()
	out, err := ofit.Dress(outfits, base, def)
	figOtel.RecordOutfitDress(context.Background(), time.Since(start))
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
func (h *handlers) dressParams(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
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
		if err := h.refuseBoxOutfits(ctx, req.Outfits, false); err != nil {
			return params, err
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
		if err := h.refuseBoxOutfits(ctx, req.Form.Outfits, false); err != nil {
			return params, err
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
		if err := h.refuseBoxOutfits(ctx, req.Outfits, false); err != nil {
			return params, err
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

var outfitGrantRoots = []string{form.ScopesKey, form.RetiredGrantsKey, "system.fanout", form.ChildPolicyKey}

func (h *handlers) refuseOutfitGrants(ctx context.Context, outfits []string) error {
	if err := h.refuseOutfitRuntimeKeys(outfits); err != nil {
		return err
	}
	return h.angelus.Policies.Shadow(authz.KeyGrants, h.refuseOutfitAuthority(ctx, outfits))
}

func (h *handlers) refuseOutfitRuntimeKeys(outfits []string) error {
	if len(outfits) == 0 {
		return nil
	}
	dressed, err := h.dress(outfits, nil)
	if err != nil {
		return err
	}
	return refuseRuntimeKeys(form.Snapshot{}, dressed)
}

func (h *handlers) refuseOutfitAuthority(ctx context.Context, outfits []string) error {
	if len(outfits) == 0 {
		return nil
	}
	dressed, err := h.dress(outfits, nil)
	if err != nil {
		return err
	}
	if c := h.callerKeysOf(ctx); c.owns && !c.boxed {
		return nil
	}
	for _, e := range dressed.Entries() {
		for _, root := range outfitGrantRoots {
			if e.Key == root || strings.HasPrefix(e.Key, root+".") {
				return fmt.Errorf("outfit %s gives a new figaro %s: only the owner names an outfit that carries it", strings.Join(outfits, ","), e.Key)
			}
		}
	}
	return nil
}

func (h *handlers) listedOutfitKeys(ctx context.Context, before form.Snapshot, outfits []string) (form.Snapshot, error) {
	box, boxed := callerBox(ctx)
	if len(outfits) == 0 {
		return before, nil
	}
	dressed, err := h.dress(outfits, nil)
	if err != nil {
		return before, err
	}
	held := map[string]json.RawMessage{}
	for _, e := range dressed.Entries() {
		own := boxed && e.Key == "system.sandbox" && e.NewString() == box
		if !e.IsRemoval() && (own || form.WriterOf(e.Key) == form.WriterOwner) {
			held[e.Key] = e.New
		}
	}
	return before.Apply(form.Build(before, held, nil)), nil
}

type requestOutfitsKey struct{}

func withRequestOutfits(ctx context.Context, method string, params json.RawMessage) context.Context {
	if !mightCarryOutfits(params) {
		return ctx
	}
	var req struct {
		Outfits []string `json:"outfits"`
		Form    *struct {
			Outfits []string `json:"outfits"`
		} `json:"form"`
	}
	if json.Unmarshal(params, &req) != nil {
		return ctx
	}
	names := req.Outfits
	if method == rpc.MethodQua && req.Form != nil {
		names = req.Form.Outfits
	}
	if len(names) == 0 {
		return ctx
	}
	return context.WithValue(ctx, requestOutfitsKey{}, names)
}

func requestOutfits(ctx context.Context) []string {
	names, _ := ctx.Value(requestOutfitsKey{}).([]string)
	return names
}
