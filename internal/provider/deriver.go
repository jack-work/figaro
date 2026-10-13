package provider

import (
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// Deriver turns a fig IR entry into what an encoder renders: the board before
// it, its patches, and its studied-form transitions. The catch-up and the
// write path share it so one channel never sees two derivations.
type Deriver struct {
	form    Form
	studies map[string]Form

	lastForm  uint64
	lastStudy map[string]uint64
	snap      form.Snapshot
}

func NewDeriver(board Form, studies map[string]Form) *Deriver {
	return &Deriver{form: board, studies: studies, lastStudy: map[string]uint64{}, snap: form.Snapshot{}}
}

// SeedAt positions the cursor at the newest translated entry, whose record
// carries the cursors.
func (d *Deriver) SeedAt(log store.Log[message.Message], watermark uint64) {
	d.lastForm = 0
	d.lastStudy = map[string]uint64{}
	d.snap = form.Snapshot{}
	if watermark == 0 {
		return
	}
	if at, ok := log.Lookup(watermark); ok {
		d.lastForm = at.FormChannelVersion
		for fid, v := range at.StudyVersions {
			d.lastStudy[fid] = v
		}
	}
	switch {
	case d.form != nil:
		if d.lastForm > 0 {
			d.snap = form.Fold(d.snap, d.form.PatchesBetween(0, d.lastForm))
		}
	default:
		// No accessor: O(entries before the watermark), on this branch only.
		prefix, _ := store.TailAfter(log, 0)
		for _, e := range prefix {
			if e.LT > watermark {
				break
			}
			d.snap = form.Fold(d.snap, e.Payload.Patches)
		}
	}
}

func (d *Deriver) At() uint64 { return d.lastForm }

// Next returns the message with its patches and study blocks, and the board
// before it. Call in log order, once per entry. It does not advance the
// cursors: an entry can encode to nothing, and its window must then ride the
// next entry that writes a row (Commit).
func (d *Deriver) Next(entry store.Entry[message.Message]) (msg message.Message, snap form.Snapshot, translatable bool) {
	msg = entry.Payload
	msg.LogicalTime = entry.LT
	if msg.Role == message.RoleGenesis {
		d.Skip(entry)
		return msg, d.snap, false
	}

	if d.form != nil {
		msg.Patches = d.form.PatchesBetween(d.lastForm, entry.FormChannelVersion)
	}

	// A window may only close on an entry that can carry the block.
	if carriesStudy(msg) {
		for fid, upTo := range entry.StudyVersions {
			acc := d.studies[fid]
			if acc == nil {
				continue
			}
			if ps := acc.PatchesBetween(d.lastStudy[fid], upTo); len(ps) > 0 {
				if msg.StudyPatches == nil {
					msg.StudyPatches = map[string][]message.Patch{}
				}
				msg.StudyPatches[fid] = ps
				if msg.StudyAt == nil {
					msg.StudyAt = map[string]uint64{}
				}
				msg.StudyAt[fid] = upTo
			}
		}
	}

	return msg, d.snap, true
}

// Commit advances the cursors past an entry whose row was written; call it
// exactly when the store accepted a row.
func (d *Deriver) Commit(entry store.Entry[message.Message], msg message.Message) {
	d.lastForm = maxVersion(d.lastForm, entry.FormChannelVersion)
	if carriesStudy(msg) {
		for fid, upTo := range entry.StudyVersions {
			d.lastStudy[fid] = maxVersion(d.lastStudy[fid], upTo)
		}
	}
	d.snap = form.Fold(d.snap, msg.Patches)
}

// Skip advances the cursors past an untranslatable entry (genesis).
func (d *Deriver) Skip(entry store.Entry[message.Message]) {
	d.lastForm = maxVersion(d.lastForm, entry.FormChannelVersion)
	for fid, upTo := range entry.StudyVersions {
		d.lastStudy[fid] = maxVersion(d.lastStudy[fid], upTo)
	}
}
