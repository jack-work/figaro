package cli

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk"
)

func (in *interactiveInput) followCatalog() {
	if in.acli == nil || in.replaying || tapeOnly.Load() || in.catalog == nil {
		return
	}
	goSettled(func() {
		attach := func() bool {
			ctx, cancel := context.WithTimeout(in.life, 5*time.Second)
			defer cancel()
			_, err := in.acli.Attach(ctx, models.PlanUsageFormID)
			return err == nil
		}
		if !attach() {
			return
		}
		var f *followed
		seed := func() {
			if f == nil {
				return
			}
			ctx, cancel := context.WithTimeout(in.life, 5*time.Second)
			defer cancel()
			resp, err := f.aria().Form(ctx)
			if err != nil {
				return
			}
			in.catalog.reset(resp.Snapshot, resp.Incarnation, resp.Version)
			in.recomputeLimit()
		}
		var err error
		f, err = followAcrossRestarts(in.life, ariaAddr(models.PlanUsageFormID), in.applyCatalogNotify(seed), func(*sdk.Aria) {
			attach()
			seed()
		})
		if err != nil {
			return
		}
		seed()
	})
}

func (in *interactiveInput) applyCatalogNotify(resync func()) sdk.NotifyHandler {
	return func(method string, params json.RawMessage) {
		if method != rpc.MethodFormDelta {
			return
		}
		var d rpc.FormDelta
		if json.Unmarshal(params, &d) != nil || d.Intrinsic != "" {
			return
		}
		in.applyCatalogDelta(d, resync)
	}
}

func (in *interactiveInput) applyCatalogDelta(d rpc.FormDelta, resync func()) {
	switch in.catalog.apply(d) {
	case formResync:
		goSettled(resync)
		return
	case formIncompatible:
		return
	}
	in.recomputeLimit()
}

func publishedPlanUsage(catalog, bound form.Snapshot) *models.PlanUsage {
	provider, model := models.Subject(bound)
	return models.PlanUsageIn(catalog, provider, model)
}
