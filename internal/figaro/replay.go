package figaro

import (
	"errors"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

func (a *Agent) SubmitReplay(inquiry message.Message, window form.Patch) error {
	evt := replayEvent(inquiry)
	if evt.text == "" && len(evt.images) == 0 {
		return errors.New("the recorded inquiry holds nothing to ask")
	}
	if !window.IsIdentity() {
		if _, err := a.backend.ApplyForm(a.id, window); err != nil {
			return err
		}
		a.form.Apply(window)
	}
	if !a.inbox.Send(evt) {
		return ErrStopping
	}
	if !a.turnActive() {
		a.publishRuntime(rpc.RuntimeAccepted, "")
	}
	return nil
}

func replayEvent(inquiry message.Message) event {
	evt := event{typ: eventUserPrompt, replay: true}
	var texts []string
	for _, c := range inquiry.Content {
		switch c.Type {
		case message.ContentProse:
			if c.Text == "" {
				continue
			}
			evt.segments = append(evt.segments, promptSegment{sender: c.Sender, principal: c.Principal, text: c.Text})
			texts = append(texts, c.Text)
		case message.ContentImage:
			if c.ToolCallID == "" {
				evt.images = append(evt.images, c)
			}
		}
	}
	evt.text = strings.Join(texts, "\n\n")
	return evt
}

type replayView struct {
	formView
	rec form.Replay
}

func boundView(backend store.Backend, id string, snap form.Snapshot) provider.Form {
	v := formView{backend: backend, id: id}
	if rec, ok := form.ReadReplay(snap); ok {
		return replayView{formView: v, rec: rec}
	}
	return v
}

func (v replayView) PatchesBetween(after, upTo uint64) []message.Patch {
	ps := v.formView.PatchesBetween(after, upTo)
	if len(ps) == 0 {
		return ps
	}
	out := make([]message.Patch, 0, len(ps))
	for _, p := range ps {
		out = append(out, v.rec.Mask(v.id, p))
	}
	return out
}

func (v replayView) StateAt(version uint64) (form.Snapshot, bool) {
	snap, ok := v.formView.StateAt(version)
	if !ok {
		return snap, false
	}
	return v.rec.MaskSnapshot(v.id, snap), true
}

type frozenStudy struct {
	provider.Form
	at uint64
}

func (f frozenStudy) PatchesBetween(after, upTo uint64) []message.Patch {
	upTo = min(upTo, f.at)
	if upTo <= after {
		return nil
	}
	return f.Form.PatchesBetween(after, upTo)
}

func freezeStudies(snap form.Snapshot, studies map[string]provider.Form) map[string]provider.Form {
	rec, ok := form.ReadReplay(snap)
	if !ok {
		return studies
	}
	for id, acc := range studies {
		studies[id] = frozenStudy{Form: acc, at: rec.Studies[id]}
	}
	return studies
}
