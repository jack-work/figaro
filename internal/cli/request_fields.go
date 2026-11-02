package cli

import (
	"context"
	"reflect"

	"github.com/jack-work/figaro/api/rpc"
)

var requestFieldCapabilities = map[string]rpc.Capability{
	"QuaRequest.Images":  rpc.CapQuaImages,
	"SetRequest.Seq":     rpc.CapFormSeq,
	"SetRequest.Client":  rpc.CapFormSeq,
	"FormRequest.Client": rpc.CapFormSeq,
}

type quaer interface {
	QuaWith(ctx context.Context, req rpc.QuaRequest) (int, bool, error)
}

func quaWith(ctx context.Context, a quaer, req rpc.QuaRequest) (int, bool, error) {
	if err := refuseUnknownFields(req); err != nil {
		return 0, false, err
	}
	return a.QuaWith(ctx, req)
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
