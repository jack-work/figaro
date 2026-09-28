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
// translated. The entry at the watermark carries the study cursors, so that
// half of the position is read from the log rather than carried between calls.
//
// consumedBoard is the OTHER half, and it comes from the translator ROW
// (Entry.BoardVersion), not from the record. A form patch may be deferred
// past a record that cannot carry it, so the record's own stamp answers "how
// far had the board moved by then" and not "how much of it has been
// rendered". Those were the same number until the window could stay open, and
// seeding from the record is how a deferred patch was lost across a restart.
func (d *Deriver) SeedAt(log store.Log[message.Message], watermark, consumedBoard uint64) {
	d.lastForm = 0
	d.lastStudy = map[string]uint64{}
	d.snap = form.Snapshot{}
	if watermark == 0 {
		return
	}
	d.lastForm = consumedBoard
	if at, ok := log.Lookup(watermark); ok {
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

// Consumes is what At WOULD report if this entry's row were written now. It
// is what the row records, so a cold start can resume the cursor: the caller
// needs the number before Commit, because the row is written first and the
// commit is what the write means.
func (d *Deriver) Consumes(entry store.Entry[message.Message], msg message.Message) uint64 {
	if !carriesForm(msg) {
		return d.lastForm
	}
	return maxVersion(d.lastForm, entry.FormChannelVersion)
}

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

	if d.form != nil && carriesForm(msg) {
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
	// THE CURSOR ONLY MOVES FOR A RECORD THAT COULD CARRY THE BLOCK, which is
	// the same rule Next applies when it decides whether to attach one. An
	// entry that was never offered the window must not consume it: advancing
	// here is how a patch attached to an assistant record came to be folded
	// into the board and rendered to nobody.
	if carriesForm(msg) {
		d.lastForm = maxVersion(d.lastForm, entry.FormChannelVersion)
	}
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
