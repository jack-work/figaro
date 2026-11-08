package angelus

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

const credoKey = "system.credo"

var errCredoFixed = errors.New("the credo is fixed at birth: fork with -S system.credo=... or succeed")

func (h *handlers) refuseCredoChange(method, id string, params json.RawMessage) error {
	patch := formPatchOf(method, params)
	if patch == nil || !mayTouchCredo(*patch) || h.angelus.Backend == nil {
		return nil
	}
	before, err := h.angelus.Backend.FormState(id)
	if err != nil {
		return err
	}
	if !changesCredo(before, before.Apply(*patch)) {
		return nil
	}
	log, err := h.angelus.Backend.OpenFigIR(id)
	if err != nil {
		return err
	}
	if hasSpoken(log) {
		return errCredoFixed
	}
	return nil
}

func hasSpoken(log store.Log[message.Message]) bool {
	before := ^uint64(0)
	for {
		page, _ := log.ReadPage(0, before, 64)
		if len(page) == 0 {
			return false
		}
		for i := len(page) - 1; i >= 0; i-- {
			if !message.IsCeremonial(page[i].Payload) {
				return true
			}
		}
		if page[0].LT == 0 {
			return false
		}
		before = page[0].LT
	}
}

func formPatchOf(method string, params json.RawMessage) *form.Patch {
	switch method {
	case rpc.MethodSet:
		var req rpc.SetRequest
		if json.Unmarshal(params, &req) != nil {
			return nil
		}
		return &req.Patch
	case rpc.MethodQua:
		var req rpc.QuaRequest
		if json.Unmarshal(params, &req) != nil || req.Form == nil {
			return nil
		}
		return req.Form.Patch
	}
	return nil
}

func mayTouchCredo(p form.Patch) bool {
	for _, e := range p.Entries() {
		if e.Key == "system" || e.Key == credoKey || strings.HasPrefix(e.Key, credoKey+".") {
			return true
		}
	}
	return false
}

func changesCredo(before, after form.Snapshot) bool {
	for _, p := range changedPaths(before, after) {
		if p == credoKey || strings.HasPrefix(p, credoKey+".") {
			return true
		}
	}
	return false
}
