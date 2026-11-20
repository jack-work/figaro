package cli

import (
	"context"
	"reflect"

	"github.com/jack-work/figaro/api/rpc"
	figOtel "github.com/jack-work/figaro/internal/otel"
)

var requestFieldCapabilities = map[string]rpc.Capability{
	"SeatRequest.FigaroID":            rpc.CapSeat,
	"SeatRequest.Successor":           rpc.CapSeat,
	"PlaceRequest.FigaroID":           rpc.CapPlace,
	"PlaceRequest.Under":              rpc.CapPlace,
	"QuaRequest.Images":               rpc.CapQuaImages,
	"SetRequest.Seq":                  rpc.CapFormSeq,
	"SetRequest.Client":               rpc.CapFormSeq,
	"FormRequest.Client":              rpc.CapFormSeq,
	"NormalizeRequest.DryRun":         rpc.CapNormalize,
	"StudyRequest.Keys":               rpc.CapStudyKeys,
	"DecideRequest.Outfit":            rpc.CapDecideOutfit,
	"FormApplyRequest.FigaroID":       rpc.CapFormApply,
	"FormApplyRequest.Outfits":        rpc.CapFormApply,
	"FormApplyRequest.Managed":        rpc.CapFormApply,
	"FormApplyRequest.IfVersion":      rpc.CapFormApply,
	"FormApplyRequest.ExpectedOutfit": rpc.CapFormApply,
	"FormApplyRequest.Check":          rpc.CapFormApply,
	"ForkRequest.Studies":             rpc.CapForkStudies,
	"ForkRequest.Seat":                rpc.CapForkSeat,
	"ReadRequest.Until":               rpc.CapReadUntil,
	"QuaRequest.Exact":                rpc.CapLogExact,
}

type quaer interface {
	ID() string
	QuaWith(ctx context.Context, req rpc.QuaRequest) (int, bool, error)
}

func quaWith(ctx context.Context, a quaer, req rpc.QuaRequest) (int, bool, error) {
	req, err := settleExact(ctx, a, req)
	if err != nil {
		return 0, false, err
	}
	if err := refuseUnknownFields(req); err != nil {
		return 0, false, err
	}
	ctx, span := startSubmit(ctx, a.ID(), len(req.Text))
	req.TraceParent = figOtel.TraceParent(ctx)
	cursor, active, err := a.QuaWith(ctx, req)
	span.AddEvent("rpc.acked")
	if err != nil {
		figOtel.RecordError(ctx, "cli.submit.error", err)
		span.End()
		return cursor, active, err
	}
	awaitFirstPaint(a.ID(), cursor, span)
	return cursor, active, err
}

func refuseUnknownFields(req any) error {
	v := reflect.ValueOf(req)
	t := v.Type()
	for i := range t.NumField() {
		c, gated := requestFieldCapabilities[t.Name()+"."+t.Field(i).Name]
		if !gated || v.Field(i).IsZero() {
			continue
		}
		if err := needCapability(c, t.Name()+"."+t.Field(i).Name); err != nil {
			return err
		}
	}
	return nil
}
