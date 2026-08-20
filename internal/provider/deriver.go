package provider

import (
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// Deriver turns a stamped fig IR entry into the message an encoder renders:
// the board as it stood BEFORE that entry, the form patches the entry
// introduced, and the studied forms' transitions it may carry.
//
// THERE IS ONE OF THESE AND TWO CALLERS, which is the point. A catch-up walks
// a suffix and calls Next per entry; the fig IR write path holds one per
// (aria, provider) and calls Next as each entry lands. Two derivations would
// be two answers to "what did the board look like then", and the two paths
// write to the same channel.
//
// IT IS A CURSOR, NOT A CACHE. Everything it holds is recoverable from the
// logs -- SeedAt rebuilds it from the entry a watermark names -- and it holds
// no encoded bytes at all.
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

// SeedAt positions the cursor at a watermark: the newest fig IR entry that has
// already been translated. THE ENTRY AT THE WATERMARK CARRIES THE CURSORS --
// its FormChannelVersion and StudyVersions are where the boards stood when it
// was written -- so the position is read from the log rather than carried
// between calls.
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
		// The board as it stood at the watermark, folded from zero: one patch
		// application per form patch, measured across a real store at p50=5,
		// p99=74, max=371.
		if d.lastForm > 0 {
			d.snap = form.Fold(d.snap, d.form.PatchesBetween(0, d.lastForm))
		}
	default:
		// AN EPHEMERAL ARIA HAS NO ACCESSOR AND CARRIES ITS PATCHES ON THE
		// ENTRIES THEMSELVES, so the board it reached is only recoverable by
		// folding the entries already translated. Skipping them left the
		// snapshot at zero and rendered a transition from nothing: "=>new"
		// where the aria had said "old=>new".
		//
		// COMPLEXITY, NAMED: O(entries before the watermark), and ONLY where
		// there is no form channel to ask.
		prefix, _ := store.TailAfter(log, 0)
		for _, e := range prefix {
			if e.LT > watermark {
				break
			}
			d.snap = form.Fold(d.snap, e.Payload.Patches)
		}
	}
}

// At reports the cursor's position: the newest fig IR LT it has consumed is
// not tracked, but the board version is, which is what a caller needs to know
// whether the cursor is stale for an entry.
func (d *Deriver) At() uint64 { return d.lastForm }

// Next consumes one entry and returns what to encode: the message with its
// patches and study blocks attached, and the board as it stood BEFORE it.
// translatable is false for entries that advance the cursors and render to
// nothing -- genesis is furniture.
//
// IT MUST BE CALLED IN LOG ORDER, EXACTLY ONCE PER ENTRY. Skipping one loses
// the patches it introduced; repeating one renders them twice.
func (d *Deriver) Next(entry store.Entry[message.Message]) (msg message.Message, snap form.Snapshot, translatable bool) {
	msg = entry.Payload
	msg.LogicalTime = entry.LT
	if msg.Role == message.RoleGenesis {
		d.lastForm = maxVersion(d.lastForm, entry.FormChannelVersion)
		for fid, upTo := range entry.StudyVersions {
			d.lastStudy[fid] = maxVersion(d.lastStudy[fid], upTo)
		}
		return msg, d.snap, false
	}

	if d.form != nil {
		// (after, upTo]: the previous entry's mark and this one's.
		msg.Patches = d.form.PatchesBetween(d.lastForm, entry.FormChannelVersion)
	}
	d.lastForm = maxVersion(d.lastForm, entry.FormChannelVersion)

	// A WINDOW MAY ONLY CLOSE ON AN ENTRY THAT CAN CARRY THE BLOCK. A studied
	// form's transitions ride a USER message: every encoder renders them under
	// RoleInput and nowhere else. An assistant entry that consumed its window
	// would compute a block, drop it, and leave the next user entry asking for
	// (v, v] -- so the change would never be shown to anyone, ever.
	if carriesStudy(msg) {
		for fid, upTo := range entry.StudyVersions {
			// ADVANCE FIRST, whatever happens below: the cursor is where the
			// form STOOD at this stamp, which is true of a form that has since
			// been deleted too.
			prev := d.lastStudy[fid]
			d.lastStudy[fid] = maxVersion(prev, upTo)
			acc := d.studies[fid]
			if acc == nil {
				continue
			}
			if ps := acc.PatchesBetween(prev, upTo); len(ps) > 0 {
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

	// The board the ENCODER sees is the one this entry arrived at, before its
	// own patches are folded: an encoder renders a transition as
	// old -> new, and old is what it needs.
	before := d.snap
	d.snap = form.Fold(d.snap, msg.Patches)
	return msg, before, true
}
