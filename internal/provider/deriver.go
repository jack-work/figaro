package provider

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// Deriver turns a fig IR entry into what an encoder renders: the bound form before
// it, its patches, and its studied-form transitions. The catch-up and the
// write path share it so one channel never sees two derivations.
type Deriver struct {
	form    Form
	studies map[string]Form

	lastForm  uint64
	lastStudy map[string]uint64
	snap      form.Snapshot
}

func NewDeriver(bound Form, studies map[string]Form) *Deriver {
	return &Deriver{form: bound, studies: studies, lastStudy: map[string]uint64{}, snap: form.Snapshot{}}
}

type formStater interface {
	StateAt(version uint64) (form.Snapshot, bool)
}

func (d *Deriver) SeedAt(log store.Log[message.Message], rows store.Log[[]json.RawMessage]) {
	d.lastForm = 0
	d.lastStudy = map[string]uint64{}
	d.snap = form.Snapshot{}
	if rows == nil {
		return
	}
	watermark := SeedLT(rows)
	if watermark == 0 {
		return
	}
	if at, ok := lastCommittedAtOrBefore(log, rows, watermark); ok {
		d.lastForm = at.FormChannelVersion
		for fid, v := range at.StudyVersions {
			d.lastStudy[fid] = v
		}
	}
	switch {
	case d.form != nil:
		if d.lastForm == 0 {
			break
		}
		if s, ok := d.form.(formStater); ok {
			if snap, ok := s.StateAt(d.lastForm); ok {
				d.snap = snap
				break
			}
		}
		d.snap = form.Fold(d.snap, d.form.PatchesBetween(0, d.lastForm))
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

func lastCommittedAtOrBefore(log store.Log[message.Message], rows store.Log[[]json.RawMessage], lt uint64) (store.Entry[message.Message], bool) {
	behind := rowsBehind{rows: rows}
	for ; lt > 0; lt-- {
		at, ok := log.Lookup(lt)
		if !ok {
			return store.Entry[message.Message]{}, false
		}
		if at.Payload.Role == message.RoleGenesis || carriesBlocks(at.Payload) && behind.carriesBytes(lt) {
			return at, true
		}
	}
	return store.Entry[message.Message]{}, false
}

type rowsBehind struct {
	rows    store.Log[[]json.RawMessage]
	page    []store.Entry[[]json.RawMessage]
	before  uint64
	reach   int
	started bool
	spent   bool
}

const rowsBehindReach = 32

func (r *rowsBehind) carriesBytes(figaroLT uint64) bool {
	for {
		for len(r.page) > 0 {
			newest := r.page[len(r.page)-1]
			if newest.FigaroLT > figaroLT {
				r.page = r.page[:len(r.page)-1]
				continue
			}
			return newest.FigaroLT == figaroLT && len(newest.Payload) > 0
		}
		if r.spent {
			return false
		}
		if !r.started {
			r.started = true
			tail, ok := r.rows.PeekTail()
			if !ok {
				r.spent = true
				return false
			}
			r.page, r.before = []store.Entry[[]json.RawMessage]{tail}, tail.LT
			continue
		}
		r.reach = min(max(2*r.reach, 1), rowsBehindReach)
		page, _ := r.rows.ReadPage(0, r.before, r.reach)
		if len(page) == 0 {
			r.spent = true
			return false
		}
		r.page, r.before = page, page[0].LT
	}
}

// Next returns the message with its patches and study blocks, and the bound form
// before it. Call in log order, once per entry. It does not advance the
// cursors: an entry can encode to nothing, and its window must then ride the
// next entry that carries bytes (Commit).
func (d *Deriver) Next(entry store.Entry[message.Message]) (msg message.Message, snap form.Snapshot, translatable bool) {
	msg = entry.Payload
	msg.LogicalTime = entry.LT
	if msg.Role == message.RoleGenesis {
		d.Skip(entry)
		return msg, d.snap, false
	}

	if d.form != nil && carriesBlocks(msg) {
		msg.Patches = d.form.PatchesBetween(d.lastForm, entry.FormChannelVersion)
	}

	// A window may only close on an entry that can carry the block.
	if carriesBlocks(msg) {
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
	if !carriesBlocks(msg) {
		return
	}
	d.lastForm = maxVersion(d.lastForm, entry.FormChannelVersion)
	for fid, upTo := range entry.StudyVersions {
		d.lastStudy[fid] = maxVersion(d.lastStudy[fid], upTo)
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
