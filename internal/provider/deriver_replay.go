package provider

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/turns"
)

func opensSpan(m message.Message) bool { return m.Replay != nil || turns.Opens(m) }

func (d *Deriver) shown(ps []message.Patch) ([]message.Patch, *form.Snapshot) {
	if d.replay == nil && d.real == nil {
		return ps, nil
	}
	real := d.snap
	if d.real != nil {
		real = *d.real
	}
	real = form.Fold(real, ps)
	p := replayView(real, d.snap, d.replay).Diff(d.snap)
	if p.IsIdentity() {
		return nil, &real
	}
	return []message.Patch{p}, &real
}

func (d *Deriver) settle() {
	if d.nextReal == nil {
		return
	}
	if d.replay == nil {
		d.real = nil
	} else {
		d.real = d.nextReal
	}
	d.nextReal = nil
}

func (d *Deriver) studyBound(fid string, upTo uint64) uint64 {
	if d.replay == nil || !d.replay.Clock {
		return upTo
	}
	at, ok := d.replay.Studies[fid]
	if !ok {
		return min(upTo, d.lastStudy[fid])
	}
	return min(upTo, max(at, d.lastStudy[fid]))
}

func replayView(real, believed form.Snapshot, r *message.Replay) form.Snapshot {
	if r == nil {
		return real
	}
	out := real
	if r.Identity && r.AriaID != "" {
		id, _ := json.Marshal(r.AriaID)
		out = out.SetPath("aria_id", id)
	}
	if r.Notice {
		if v, ok := believed.Get(form.ForkedFromKey); ok {
			out = out.SetPath(form.ForkedFromKey, v)
		} else if out.Has(form.ForkedFromKey) {
			out = out.DeletePath(form.ForkedFromKey)
		}
	}
	return out
}

func (d *Deriver) seedReplay(log arialog.Log[message.Message], through uint64) {
	var mark *message.Replay
	var before uint64
	for lt := through; lt > 0; lt-- {
		e, ok := log.Lookup(lt)
		if !ok {
			return
		}
		if !opensSpan(e.Payload) {
			continue
		}
		if e.Payload.Replay == nil {
			break
		}
		if mark == nil {
			mark = e.Payload.Replay
		}
		before = 0
		if prev, ok := log.Lookup(lt - 1); ok {
			before = prev.FormChannelVersion
		}
	}
	if mark == nil {
		return
	}
	real := d.snap
	held := d.stateAt(before)
	d.replay = mark
	d.snap = replayView(real, held, mark)
	d.real = &real
	if mark.Clock {
		for fid, at := range mark.Studies {
			if v, ok := d.lastStudy[fid]; ok && v > at {
				d.lastStudy[fid] = at
			}
		}
	}
}

func (d *Deriver) stateAt(version uint64) form.Snapshot {
	if version == 0 {
		return form.Snapshot{}
	}
	if s, ok := d.form.(formStater); ok {
		if snap, ok := s.StateAt(version); ok {
			return snap
		}
	}
	return form.Fold(form.Snapshot{}, d.form.PatchesBetween(0, version))
}
