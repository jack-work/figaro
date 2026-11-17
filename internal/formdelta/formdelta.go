// Package formdelta derives, per IR record, what changed on the bound form
// and on every studied form.
package formdelta

import (
	"slices"
	"strings"

	"github.com/jack-work/figaro/internal/arialog"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
)

type Backend interface {
	FormPatchesBetween(ariaID string, after, upTo uint64) ([]store.VersionedPatch, error)
	Libretto(sourceFormID string) (*store.Libretto, error)
}

// Seed is the stamps of the record preceding a window. A backward page that
// omits it attributes everything before its first record to that record.
type Seed struct {
	Form    uint64
	Studies map[string]uint64
}

func SeedFrom(e arialog.Entry[message.Message]) Seed {
	s := Seed{Form: e.FormChannelVersion}
	if len(e.StudyVersions) > 0 {
		s.Studies = make(map[string]uint64, len(e.StudyVersions))
		for k, v := range e.StudyVersions {
			s.Studies[k] = v
		}
	}
	return s
}

// PerRecord returns each record's deltas keyed by LT, then "<formid>.<path>".
func PerRecord(b Backend, ariaID string, entries []arialog.Entry[message.Message]) map[uint64]map[string]livedoc.FormDelta {
	return PerRecordFrom(b, ariaID, Seed{}, entries)
}

type versionSpan struct {
	minAfter uint64
	maxUpTo  uint64
	seen     bool
}

func (s *versionSpan) add(after, upTo uint64) {
	if !s.seen {
		s.minAfter = after
		s.maxUpTo = upTo
		s.seen = true
		return
	}
	if after < s.minAfter {
		s.minAfter = after
	}
	if upTo > s.maxUpTo {
		s.maxUpTo = upTo
	}
}

func scanSpans(seed Seed, entries []arialog.Entry[message.Message]) (versionSpan, map[string]versionSpan) {
	var bound versionSpan
	var studies map[string]versionSpan
	curForm := seed.Form
	curStudy := make(map[string]uint64, len(seed.Studies))
	for k, v := range seed.Studies {
		curStudy[k] = v
	}
	for _, entry := range entries {
		if entry.Payload.Role == message.RoleGenesis {
			curForm = maxU64(curForm, entry.FormChannelVersion)
			for fid, upTo := range entry.StudyVersions {
				curStudy[fid] = maxU64(curStudy[fid], upTo)
			}
			continue
		}
		if entry.FormChannelVersion > curForm {
			bound.add(curForm, entry.FormChannelVersion)
			curForm = entry.FormChannelVersion
		}
		for fid, upTo := range entry.StudyVersions {
			prev := curStudy[fid]
			curStudy[fid] = maxU64(prev, upTo)
			if upTo <= prev {
				continue
			}
			if studies == nil {
				studies = map[string]versionSpan{}
			}
			sp := studies[fid]
			sp.add(prev, upTo)
			studies[fid] = sp
		}
	}
	return bound, studies
}

func loadBoundSpan(b Backend, ariaID string, sp versionSpan) ([]store.VersionedPatch, bool) {
	if !sp.seen {
		return nil, true
	}
	ps, err := b.FormPatchesBetween(ariaID, sp.minAfter, sp.maxUpTo)
	return ps, err == nil
}

func PerRecordFrom(b Backend, ariaID string, seed Seed, entries []arialog.Entry[message.Message]) map[uint64]map[string]livedoc.FormDelta {
	if b == nil || ariaID == "" || len(entries) == 0 {
		return nil
	}
	boundSpan, studySpans := scanSpans(seed, entries)
	boundPatches, boundComplete := loadBoundSpan(b, ariaID, boundSpan)

	out := map[uint64]map[string]livedoc.FormDelta{}
	lastForm := seed.Form
	lastStudy := make(map[string]uint64, len(seed.Studies))
	for k, v := range seed.Studies {
		lastStudy[k] = v
	}
	studied := map[string]*studiedForm{}
	observer := observerState{b: b, id: ariaID}

	for _, entry := range entries {
		if entry.Payload.Role == message.RoleGenesis {
			lastForm = maxU64(lastForm, entry.FormChannelVersion)
			for fid, upTo := range entry.StudyVersions {
				lastStudy[fid] = maxU64(lastStudy[fid], upTo)
			}
			continue
		}

		var deltas map[string]livedoc.FormDelta

		if entry.FormChannelVersion > lastForm {
			var ps []store.VersionedPatch
			if boundComplete {
				ps = store.SlicePatches(boundPatches, lastForm, entry.FormChannelVersion)
			} else {
				ps = boundBetween(b, ariaID, lastForm, entry.FormChannelVersion)
			}
			if len(ps) > 0 && deltas == nil {
				deltas = map[string]livedoc.FormDelta{}
			}
			for _, vp := range ps {
				foldPatch(deltas, ariaID, livedoc.FormBound, vp.Patch)
				observer.advance(vp.Patch)
			}
			lastForm = entry.FormChannelVersion
		}

		// Read through the libretto: the copy outlives its source and is the
		// only place a dead form's transitions exist.
		for fid, upTo := range entry.StudyVersions {
			prev := lastStudy[fid]
			lastStudy[fid] = maxU64(prev, upTo)
			if upTo <= prev {
				continue
			}
			sf := studied[fid]
			if sf == nil {
				sf = openStudied(b, fid, studySpans[fid])
				studied[fid] = sf
			}
			if sf.lib == nil {
				continue
			}
			keys := observer.keysAt(entry.FormChannelVersion, fid)
			var ps []store.VersionedPatch
			if sf.complete {
				ps = store.SlicePatches(sf.patches, prev, upTo)
			} else {
				ps = studiedBetween(sf.lib, prev, upTo)
			}
			if len(ps) > 0 && deltas == nil {
				deltas = map[string]livedoc.FormDelta{}
			}
			for _, vp := range ps {
				foldStudied(deltas, fid, sf, keys, vp.Patch)
			}
		}

		if len(deltas) > 0 {
			out[entry.LT] = deltas
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func boundBetween(b Backend, ariaID string, after, upTo uint64) []store.VersionedPatch {
	ps, err := b.FormPatchesBetween(ariaID, after, upTo)
	if err == nil {
		return ps
	}
	at, ok := b.(formStateAt)
	if !ok {
		return nil
	}
	return diffBetween(func(v uint64) (form.Snapshot, error) { return at.FormStateAt(ariaID, v) }, after, upTo)
}

func studiedBetween(lib *store.Libretto, after, upTo uint64) []store.VersionedPatch {
	ps, err := lib.PatchesBetween(after, upTo)
	if err == nil {
		return ps
	}
	return diffBetween(lib.StateAt, after, upTo)
}

func diffBetween(stateAt func(uint64) (form.Snapshot, error), after, upTo uint64) []store.VersionedPatch {
	to, err := stateAt(upTo)
	if err != nil {
		return nil
	}
	from := form.Snapshot{}
	if after > 0 {
		if from, err = stateAt(after); err != nil {
			return nil
		}
	}
	if p := to.Diff(from); !p.IsIdentity() {
		return []store.VersionedPatch{{Version: upTo, Patch: p}}
	}
	return nil
}

// role is decided once per assembly from the copy's current state: a form
// that became a role mid-history renders as a role throughout.
type studiedForm struct {
	lib      *store.Libretto
	role     bool
	patches  []store.VersionedPatch
	complete bool
}

func openStudied(b Backend, fid string, sp versionSpan) *studiedForm {
	lib, err := b.Libretto(fid)
	if err != nil || lib == nil {
		return &studiedForm{}
	}
	sf := &studiedForm{lib: lib, complete: true}
	if _, ok := lib.State().Get("target-aria"); ok {
		sf.role = true
	}
	if sp.seen {
		ps, err := lib.PatchesBetween(sp.minAfter, sp.maxUpTo)
		sf.patches, sf.complete = ps, err == nil
	}
	return sf
}

func foldPatch(deltas map[string]livedoc.FormDelta, formID string, kind livedoc.FormKind, p message.Patch) {
	for _, ent := range p.Entries() {
		k, v := ent.Key, ent.New
		if ent.IsRemoval() {
			continue
		}
		put(deltas, formID+"."+k, livedoc.FormDelta{
			Value: v, Prev: ent.Old, Kind: kind, Event: livedoc.FormSet, Form: formID,
		})
	}
	for _, ent := range p.Entries() {
		if !ent.IsRemoval() {
			continue
		}
		k := ent.Key
		put(deltas, formID+"."+k, livedoc.FormDelta{
			Prev: ent.Old, Kind: kind, Event: livedoc.FormRemoved, Form: formID,
		})
	}
}

// foldStudied strips libretto machinery; the death arrives as FormDeleted on
// the form itself rather than as a key.
func foldStudied(deltas map[string]livedoc.FormDelta, fid string, sf *studiedForm, keys []string, p message.Patch) {
	kind := livedoc.FormStudied
	if sf.role {
		kind = livedoc.FormRole
	}
	for _, ent := range p.Entries() {
		k, v := ent.Key, ent.New
		if ent.IsRemoval() {
			continue
		}
		if k == form.KeyLibrettoAlive {
			if strings.TrimSpace(string(v)) == "false" {
				deltas[fid] = livedoc.FormDelta{Kind: kind, Event: livedoc.FormDeleted, Form: fid}
			}
			continue
		}
		if store.HiddenLibrettoKey(k) || !form.StudyKeyShown(k, keys) {
			continue
		}
		put(deltas, fid+"."+k, livedoc.FormDelta{Value: v, Prev: ent.Old, Kind: kind, Event: livedoc.FormSet, Form: fid})
	}
	for _, ent := range p.Entries() {
		if !ent.IsRemoval() {
			continue
		}
		k := ent.Key
		if store.HiddenLibrettoKey(k) || k == form.KeyLibrettoAlive || !form.StudyKeyShown(k, keys) {
			continue
		}
		put(deltas, fid+"."+k, livedoc.FormDelta{Prev: ent.Old, Kind: kind, Event: livedoc.FormRemoved, Form: fid})
	}
}

func maxU64(a, b uint64) uint64 {
	if b > a {
		return b
	}
	return a
}

// Attach folds per-record deltas onto the node projected from the record
// carrying the stamp; a record that projects no node attaches to the turn.
func Attach(turns []aria.Turn, deltas map[uint64]map[string]livedoc.FormDelta) {
	if len(deltas) == 0 || len(turns) == 0 {
		return
	}
	lts := orderedLTs(deltas)
	for ti := range turns {
		t := &turns[ti]
		if len(t.LTs) < 2 || t.LTs[1] < t.LTs[0] {
			continue
		}
		start, _ := slices.BinarySearch(lts, t.LTs[0])
		if start >= len(lts) || lts[start] > t.LTs[1] {
			continue
		}
		// First node to claim an LT wins: one record's state renders once.
		claimed := map[uint64]int{}
		var lastClaimed uint64
		for ni := range t.Nodes {
			for _, src := range t.Nodes[ni].Src {
				if _, ok := claimed[src.LT]; !ok {
					claimed[src.LT] = ni
					lastClaimed = maxU64(lastClaimed, src.LT)
				}
			}
		}
		for i := start; i < len(lts) && lts[i] <= t.LTs[1]; i++ {
			lt := lts[i]
			d := deltas[lt]
			if len(d) == 0 {
				continue
			}
			if ni, ok := claimed[lt]; ok {
				t.Nodes[ni].FormDeltas = merge(t.Nodes[ni].FormDeltas, d)
				continue
			}
			// A record that projects no node and trails every node of this
			// turn (a fork's birth record) belongs to the turn after it; with
			// no turn after it, it waits for the composition that has one
			// (Agent.stampSealDeltas on the live path).
			if lt > t.LTs[0] && lt > lastClaimed {
				if ti+1 < len(turns) {
					turns[ti+1].FormDeltas = merge(turns[ti+1].FormDeltas, d)
				}
				continue
			}
			t.FormDeltas = merge(t.FormDeltas, d)
		}
	}
}

// AttachOne is Attach for one turn, without the seam rule.
func AttachOne(t *aria.Turn, deltas map[uint64]map[string]livedoc.FormDelta) {
	if t == nil || len(deltas) == 0 {
		return
	}
	claimed := map[uint64]int{}
	for ni := range t.Nodes {
		for _, src := range t.Nodes[ni].Src {
			if _, ok := claimed[src.LT]; !ok {
				claimed[src.LT] = ni
			}
		}
	}
	for _, lt := range orderedLTs(deltas) {
		d := deltas[lt]
		if ni, ok := claimed[lt]; ok {
			t.Nodes[ni].FormDeltas = merge(t.Nodes[ni].FormDeltas, d)
			continue
		}
		t.FormDeltas = merge(t.FormDeltas, d)
	}
}

// The fold collapses, so the walk is never map order.
func orderedLTs(deltas map[uint64]map[string]livedoc.FormDelta) []uint64 {
	lts := make([]uint64, 0, len(deltas))
	for lt := range deltas {
		lts = append(lts, lt)
	}
	slices.Sort(lts)
	return lts
}

// merge: a key touched twice in one window renders as first Prev to last
// Value.
func merge(into, from map[string]livedoc.FormDelta) map[string]livedoc.FormDelta {
	if into == nil {
		into = make(map[string]livedoc.FormDelta, len(from))
	}
	for k, v := range from {
		put(into, k, v)
	}
	return into
}

func put(deltas map[string]livedoc.FormDelta, key string, d livedoc.FormDelta) {
	if prior, ok := deltas[key]; ok {
		d.Prev = prior.Prev
	}
	deltas[key] = d
}

type formStateAt interface {
	FormStateAt(ariaID string, version uint64) (form.Snapshot, error)
}

// observerState is the observer's bound form as of the record being folded,
// read once and advanced by the patches the loop already holds.
type observerState struct {
	b      Backend
	id     string
	snap   form.Snapshot
	loaded bool
}

func (o *observerState) advance(p message.Patch) {
	if o.loaded {
		o.snap = o.snap.Apply(p)
	}
}

func (o *observerState) keysAt(version uint64, fid string) []string {
	if !o.loaded {
		at, ok := o.b.(formStateAt)
		if !ok {
			return nil
		}
		snap, err := at.FormStateAt(o.id, version)
		if err != nil {
			return nil
		}
		o.snap, o.loaded = snap, true
	}
	return form.StudyKeys(o.snap, fid)
}
