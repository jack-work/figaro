package provider

import (
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// Deriver turns a stamped fig IR entry into the message an encoder renders:
// the board as it stood BEFORE that entry, the form patches the entry
// introduced, and the studied forms' transitions it may carry.
//
// One implementation, two callers -- the catch-up and the fig IR write path --
// because both write to the same channel and two derivations would be two
// answers to "what did the board look like then".
//
// It is a cursor, not a cache: SeedAt rebuilds it from the logs and it holds
// no encoded bytes.
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

// SeedAt positions the cursor at a watermark: the newest fig IR entry already
// translated. The entry at the watermark carries the cursors, so the position
// is read from the log rather than carried between calls.
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
		// No accessor: the patches ride the entries, so the board is only
		// recoverable by folding them.
		// COMPLEXITY: O(entries before the watermark), only on this branch.
		prefix, _ := store.TailAfter(log, 0)
		for _, e := range prefix {
			if e.LT > watermark {
				break
			}
			d.snap = form.Fold(d.snap, e.Payload.Patches)
		}
	}
}

// At reports the board version the cursor has consumed to.
func (d *Deriver) At() uint64 { return d.lastForm }

// Next reads one entry and returns what to encode: the message with its
// patches and study blocks attached, and the board as it stood BEFORE it.
// translatable is false for an entry that renders to nothing.
//
// IT MUST BE CALLED IN LOG ORDER, EXACTLY ONCE PER ENTRY.
//
// IT DOES NOT ADVANCE THE CURSORS. Commit does, and only the caller knows
// whether a row was actually written: an entry can be translatable, carry a
// delta, and still encode to nothing, and a cursor advanced for such an entry
// consumes a window that no row ever renders. The delta then rides the next
// entry that DOES write, which is what "look back to the most recent message
// that actually has a translation" means when the log is walked forward.
func (d *Deriver) Next(entry store.Entry[message.Message]) (msg message.Message, snap form.Snapshot, translatable bool) {
	msg = entry.Payload
	msg.LogicalTime = entry.LT
	if msg.Role == message.RoleGenesis {
		d.Skip(entry)
		return msg, d.snap, false
	}

	if d.form != nil {
		// (after, upTo]: the last COMMITTED mark and this entry's.
		msg.Patches = d.form.PatchesBetween(d.lastForm, entry.FormChannelVersion)
	}

	// A WINDOW MAY ONLY CLOSE ON AN ENTRY THAT CAN CARRY THE BLOCK.
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

	// The encoder renders old -> new, so it needs the board this entry
	// arrived at, before its own patches fold in.
	return msg, d.snap, true
}

// Commit advances the cursors past an entry whose row WAS written, and folds
// its patches into the board. Until it is called the ranges stay open, so an
// entry that encoded to nothing leaves its delta for the next entry that does
// not.
//
// A caller that never commits renders the same patches on every entry; a
// caller that commits unconditionally loses the ones that were never written.
// Both callers of Next commit exactly when the store accepted a row.
func (d *Deriver) Commit(entry store.Entry[message.Message], msg message.Message) {
	d.lastForm = maxVersion(d.lastForm, entry.FormChannelVersion)
	if carriesStudy(msg) {
		for fid, upTo := range entry.StudyVersions {
			d.lastStudy[fid] = maxVersion(d.lastStudy[fid], upTo)
		}
	}
	d.snap = form.Fold(d.snap, msg.Patches)
}

// Skip advances the cursors past an entry that is NOT translatable at all --
// genesis, and anything the deriver itself refuses. Such an entry can carry no
// block, so holding its window open would render nothing and cost a rescan.
func (d *Deriver) Skip(entry store.Entry[message.Message]) {
	d.lastForm = maxVersion(d.lastForm, entry.FormChannelVersion)
	for fid, upTo := range entry.StudyVersions {
		d.lastStudy[fid] = maxVersion(d.lastStudy[fid], upTo)
	}
}
