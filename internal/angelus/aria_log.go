package angelus

import (
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

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
