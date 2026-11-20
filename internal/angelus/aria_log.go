package angelus

import (
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

var logModelKeys = []string{"system.provider", "system.model"}

func refuseLogKeys(line lineage, before, after form.Snapshot) error {
	if line.of != "" && !line.birth && form.KindOf(before) != form.KindOf(after) {
		return fmt.Errorf("%s is fixed at birth: fork with -S %s=... for another kind", form.KindKey, form.KindKey)
	}
	if !form.IsLog(after) {
		return nil
	}
	for _, k := range logModelKeys {
		if after.Has(k) {
			return fmt.Errorf("an aria log has no model, so it carries no %s: drop the key (-D %s at a fork), or mint the log with form.bind (figaro new --log)", k, k)
		}
	}
	return nil
}

func (h *handlers) refuseModelVerbsOnLog(method, id string, params json.RawMessage) error {
	switch method {
	case rpc.MethodInterrupt:
	case rpc.MethodQua:
		var req rpc.QuaRequest
		if json.Unmarshal(params, &req) != nil || req.Text != "" || len(req.Images) > 0 {
			return nil
		}
	default:
		return nil
	}
	if h.angelus.Backend == nil {
		return nil
	}
	snap, err := h.angelus.Backend.FormState(id)
	if err != nil || !form.IsLog(snap) {
		return err
	}
	if method == rpc.MethodInterrupt {
		return fmt.Errorf("%s is an aria log: a send appends at once, so there is no turn to interrupt", id)
	}
	return fmt.Errorf("%s is an aria log: an entry needs text or an image; figaro set writes the form alone", id)
}
