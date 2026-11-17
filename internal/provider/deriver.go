package provider

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
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

	heldAt    uint64
	heldForm  bool
	heldStudy map[string]bool

	studyAt map[string]studyState
}

type studyState struct {
	version uint64
	snap    form.Snapshot
}

func NewDeriver(bound Form, studies map[string]Form) *Deriver {
	return &Deriver{form: bound, studies: studies, lastStudy: map[string]uint64{}, snap: form.Snapshot{}}
}

type formStater interface {
	StateAt(version uint64) (form.Snapshot, bool)
}

func (d *Deriver) SeedAt(log arialog.Log[message.Message], rows arialog.Log[[]json.RawMessage]) {
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
		if ps, err := d.form.PatchesBetween(0, d.lastForm); err == nil {
			d.snap = form.Fold(d.snap, ps)
		} else {
			d.lastForm = 0
		}
	default:
		// No accessor: O(entries before the watermark), on this branch only.
		prefix, _ := arialog.TailAfter(log, 0)
		for _, e := range prefix {
			if e.LT > watermark {
				break
			}
			d.snap = form.Fold(d.snap, e.Payload.Patches)
		}
	}
}

func (d *Deriver) At() uint64 { return d.lastForm }

func lastCommittedAtOrBefore(log arialog.Log[message.Message], rows arialog.Log[[]json.RawMessage], lt uint64) (arialog.Entry[message.Message], bool) {
	behind := rowsBehind{rows: rows}
	for ; lt > 0; lt-- {
		at, ok := log.Lookup(lt)
		if !ok {
			return arialog.Entry[message.Message]{}, false
		}
		if at.Payload.Role == message.RoleGenesis || carriesBlocks(at.Payload) && behind.carriesBytes(lt) {
			return at, true
		}
	}
	return arialog.Entry[message.Message]{}, false
}

type rowsBehind struct {
	rows    arialog.Log[[]json.RawMessage]
	page    []arialog.Entry[[]json.RawMessage]
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
			r.page, r.before = []arialog.Entry[[]json.RawMessage]{tail}, tail.LT
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
func (d *Deriver) Next(entry arialog.Entry[message.Message]) (msg message.Message, snap form.Snapshot, translatable bool) {
	msg = entry.Payload
	msg.LogicalTime = entry.LT
	if msg.Role == message.RoleGenesis {
		d.Skip(entry)
		return msg, d.snap, false
	}

	d.heldAt, d.heldForm, d.heldStudy = entry.LT, false, nil
	if d.form != nil && carriesBlocks(msg) {
		ps, ok := resync(d.form, d.lastForm, entry.FormChannelVersion, d.snap, true)
		msg.Patches = ps
		d.heldForm = !ok
	}

	// A window may only close on an entry that can carry the block.
	if carriesBlocks(msg) {
		for fid, upTo := range entry.StudyVersions {
			acc := d.studies[fid]
			if acc == nil {
				continue
			}
			after := d.lastStudy[fid]
			from, known := d.studyStateAt(fid, acc, after, false)
			ps, ok := resync(acc, after, upTo, from, known)
			if ok {
				ps = d.collapseStudyWindow(fid, acc, after, upTo, ps)
			}
			if !ok {
				if d.heldStudy == nil {
					d.heldStudy = map[string]bool{}
				}
				d.heldStudy[fid] = true
			}
			if len(ps) > 0 {
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
func (d *Deriver) Commit(entry arialog.Entry[message.Message], msg message.Message) {
	if !carriesBlocks(msg) {
		return
	}
	held := d.heldAt == entry.LT
	if !held || !d.heldForm {
		d.lastForm = maxVersion(d.lastForm, entry.FormChannelVersion)
	}
	for fid, upTo := range entry.StudyVersions {
		if held && d.heldStudy[fid] {
			continue
		}
		d.lastStudy[fid] = maxVersion(d.lastStudy[fid], upTo)
	}
	d.snap = form.Fold(d.snap, msg.Patches)
}

func resync(f Form, after, upTo uint64, prev form.Snapshot, known bool) ([]message.Patch, bool) {
	if upTo <= after {
		return nil, true
	}
	ps, err := f.PatchesBetween(after, upTo)
	if err == nil {
		return ps, true
	}
	s, ok := f.(formStater)
	if !ok {
		return nil, false
	}
	to, ok := s.StateAt(upTo)
	if !ok {
		return nil, false
	}
	if !known {
		prev = form.Snapshot{}
		if after > 0 {
			if from, ok := s.StateAt(after); ok {
				prev = from
			}
		}
	}
	if p := to.Diff(prev); !p.IsIdentity() {
		return []message.Patch{p}, true
	}
	return nil, true
}

// Skip advances the cursors past an untranslatable entry (genesis).
func (d *Deriver) Skip(entry arialog.Entry[message.Message]) {
	d.lastForm = maxVersion(d.lastForm, entry.FormChannelVersion)
	for fid, upTo := range entry.StudyVersions {
		d.lastStudy[fid] = maxVersion(d.lastStudy[fid], upTo)
	}
}

const studyCollapseFrom = 32

func (d *Deriver) collapseStudyWindow(fid string, f Form, after, upTo uint64, ps []message.Patch) []message.Patch {
	from, known := d.studyStateAt(fid, f, after, len(ps) >= studyCollapseFrom)
	if !known {
		return ps
	}
	to := form.Fold(from, ps)
	if d.studyAt == nil {
		d.studyAt = map[string]studyState{}
	}
	d.studyAt[fid] = studyState{version: upTo, snap: to}
	if len(ps) < studyCollapseFrom {
		return ps
	}
	diff := to.Diff(from)
	if diff.IsIdentity() {
		return nil
	}
	fold := 0
	for _, p := range ps {
		fold += p.ApproxBytes()
	}
	if diff.ApproxBytes() >= fold {
		return ps
	}
	return []message.Patch{diff}
}

func (d *Deriver) studyStateAt(fid string, f Form, at uint64, load bool) (form.Snapshot, bool) {
	if c, ok := d.studyAt[fid]; ok && c.version == at {
		return c.snap, true
	}
	if !load {
		return form.Snapshot{}, false
	}
	if at == 0 {
		return form.Snapshot{}, true
	}
	s, ok := f.(formStater)
	if !ok {
		return form.Snapshot{}, false
	}
	return s.StateAt(at)
}
