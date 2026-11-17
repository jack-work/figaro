package figaro

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/turns"
)

type ReplayRecord struct {
	LT       uint64
	Inquiry  message.Message
	AriaID   string
	Studies  map[string]uint64
	Set      map[string]json.RawMessage
	Drop     []string
	Datetime json.RawMessage
	Turn     []message.Message
}

type formStatesAt interface {
	FormStateAt(ariaID string, version uint64) (form.Snapshot, error)
}

func ResolveReplay(backend store.Backend, r form.Replay) (ReplayRecord, error) {
	if backend == nil {
		return ReplayRecord{}, errors.New("no store to read the record from")
	}
	log, err := backend.OpenFigIR(r.Of)
	if err != nil {
		return ReplayRecord{}, fmt.Errorf("open %s: %w", r.Of, err)
	}
	entries := log.Read()
	msgs := make([]message.Message, len(entries))
	for i, e := range entries {
		msgs[i] = e.Payload
		msgs[i].LogicalTime = e.LT
	}
	first, last, ok := turns.Span(msgs, r.Turn)
	if !ok {
		return ReplayRecord{}, fmt.Errorf("%s has no turn %d", r.Of, r.Turn)
	}
	if r.LT != 0 && r.LT != first {
		return ReplayRecord{}, fmt.Errorf("turn %d of %s opens at LT %d, not %d", r.Turn, r.Of, first, r.LT)
	}
	rec := ReplayRecord{LT: first}
	var base uint64
	for i, e := range entries {
		switch {
		case e.LT < first:
			base = e.FormChannelVersion
		case e.LT == first:
			if !turns.Opens(msgs[i]) {
				return ReplayRecord{}, fmt.Errorf("turn %d of %s opens with no inquiry to replay", r.Turn, r.Of)
			}
			rec.Inquiry = e.Payload
			rec.Studies = e.StudyVersions
			rec.Turn = append(rec.Turn, msgs[i])
			states, ok := backend.(formStatesAt)
			if !ok {
				return ReplayRecord{}, fmt.Errorf("this store cannot read %s's board as it stood at turn %d", r.Of, r.Turn)
			}
			before, err := states.FormStateAt(r.Of, base)
			if err != nil {
				return ReplayRecord{}, fmt.Errorf("%s's board before turn %d: %w", r.Of, r.Turn, err)
			}
			after, err := states.FormStateAt(r.Of, e.FormChannelVersion)
			if err != nil {
				return ReplayRecord{}, fmt.Errorf("%s's board at turn %d: %w", r.Of, r.Turn, err)
			}
			rec.AriaID = r.Of
			if id := after.Lookup("aria_id"); id != nil && *id != "" {
				rec.AriaID = *id
			}
			rec.Datetime, _ = after.Get(form.DatetimeKey)
			rec.Set, rec.Drop = inquiryWindow(before, after)
		case e.LT <= last:
			rec.Turn = append(rec.Turn, msgs[i])
		}
	}
	return rec, nil
}

func (rec ReplayRecord) Mark(coord form.Replay, masks form.ReplayMasks) message.Replay {
	m := message.Replay{
		Of: coord.Of, Turn: coord.Turn, LT: rec.LT,
		Notice: masks.Notice, Clock: masks.Clock, Identity: masks.Identity, Tools: masks.Tools,
	}
	if masks.Identity {
		m.AriaID = rec.AriaID
	}
	if masks.Clock {
		m.Studies = rec.Studies
	}
	return m
}

func inquiryWindow(before, after form.Snapshot) (map[string]json.RawMessage, []string) {
	keep := func(k string) bool { return k != "aria_id" && !strings.HasPrefix(k, "system.") }
	set := map[string]json.RawMessage{}
	for k, v := range after.All() {
		if !keep(k) {
			continue
		}
		if old, ok := before.Get(k); ok && form.NewValue(old).Equal(form.NewValue(v)) {
			continue
		}
		set[k] = v
	}
	var drop []string
	for k := range before.All() {
		if keep(k) && !after.Has(k) {
			drop = append(drop, k)
		}
	}
	return set, drop
}

type replayTurn struct {
	mark message.Replay
	mu   sync.Mutex
	tape *replayTape
}

func (a *Agent) SubmitReplay() error {
	if !a.inbox.Send(event{typ: eventUserPrompt, replay: true}) {
		return ErrStopping
	}
	if !a.turnActive() {
		a.publishRuntime(rpc.RuntimeAccepted, "")
	}
	return nil
}

var errReplayUnset = errors.New("system.replay was unset before the replay ran")

func (a *Agent) beginReplay(prompt event) (event, error) {
	a.replay.Store(nil)
	snap := a.form.Snapshot()
	coord, ok := form.ReadReplay(snap)
	if !ok {
		if prompt.replay && prompt.text == "" && len(prompt.images) == 0 {
			return prompt, errReplayUnset
		}
		return prompt, nil
	}
	rec, err := ResolveReplay(a.backend, coord)
	if err != nil {
		return prompt, fmt.Errorf("replay %s: %w", coord, err)
	}
	set := map[string]json.RawMessage{}
	var drop []string
	if prompt.replay {
		prompt = withInquiry(prompt, rec.Inquiry)
		for k, v := range rec.Set {
			set[k] = v
		}
		drop = rec.Drop
	}
	masks := form.ReadReplayMasks(snap)
	switch {
	case masks.Clock && len(rec.Datetime) > 0:
		set[form.DatetimeKey] = rec.Datetime
	case !masks.Clock && prompt.replay:
		set[form.DatetimeKey] = form.Datetime(time.Now())
	}
	if patch := form.Build(a.form.Snapshot(), set, drop); !patch.IsIdentity() {
		if _, err := a.backend.ApplyForm(a.id, patch); err != nil {
			return prompt, fmt.Errorf("replay %s: the recorded form window: %w", coord, err)
		}
		a.form.Apply(patch)
	}
	a.replay.Store(&replayTurn{mark: rec.Mark(coord, masks), tape: recordTape(rec.Turn)})
	return prompt, nil
}

func withInquiry(prompt event, inquiry message.Message) event {
	rec := replayEvent(inquiry)
	if prompt.text != "" && len(prompt.segments) == 0 {
		prompt.segments = []promptSegment{{text: prompt.text}}
	}
	prompt.segments = append(rec.segments, prompt.segments...)
	prompt.images = append(rec.images, prompt.images...)
	if prompt.text == "" {
		prompt.text = rec.text
	} else {
		prompt.text = rec.text + "\n\n" + prompt.text
	}
	return prompt
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

func (a *Agent) refuseAsReplay() error {
	if rec, ok := form.ReadReplay(a.form.Snapshot()); ok {
		return fmt.Errorf("%s replays turn %d of %s and takes no messages", a.id, rec.Turn, rec.Of)
	}
	return nil
}
