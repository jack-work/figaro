package angelus

import (
	"log/slog"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
)

func (a *Angelus) PublishModelFacts() {
	for _, info := range a.Registry.List() {
		if p, ok := a.Registry.Get(info.ID).(interface{ PublishModelFacts() }); ok {
			p.PublishModelFacts()
		}
	}
	if a.Hubs == nil {
		return
	}
	for _, hb := range a.Hubs.all() {
		if hb.Attached() > 0 {
			a.publishDormantModelFacts(hb)
		}
	}
}

func (a *Angelus) publishDormantModelFacts(hb *ariaHub) {
	if a.Models == nil || a.Backend == nil || a.Registry.Get(hb.id) != nil {
		return
	}
	if n, ok := a.Backend.NodeLink(hb.id); !ok || n.Kind != conversationKind {
		return
	}
	bound, err := a.Backend.FormState(hb.id)
	if err != nil {
		return
	}
	patch := figaro.ModelFactsPatch(a.Models.Snapshot(), bound)
	if patch.IsIdentity() {
		return
	}
	version, err := a.Backend.ApplyFormPrivileged(hb.id, patch)
	if err != nil {
		slog.Warn("publish model facts", "aria", hb.id, "err", err)
		return
	}
	_ = hb.Notify(rpc.MethodFormDelta, rpc.FormDelta{
		Schema: rpc.FormDeltaSchema, AriaID: hb.id, Version: version,
		Patch: patch, At: time.Now().UnixMilli(),
	})
}
