// Package formdelta derives, for each IR record, the form state a reader
// of a transcript would have needed to understand it: what changed on the
// bound board, on every studied form, and the moment a role was recast.
package formdelta

import (
	"strings"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// Backend is the slice of the store this package reads. Satisfied by
// *store.XwalBackend; an ephemeral backend has neither boards on disk nor
// librettos, and an ephemeral aria simply carries no deltas -- the field
// is absent, not synthesized from the provider path.
type Backend interface {
	FormPatchesBetween(ariaID string, after, upTo uint64) ([]store.VersionedPatch, error)
	Libretto(sourceFormID string) (*store.Libretto, error)
}

// Seed is where the cursors stood just before a window of entries: the
// stamps of the record PRECEDING it. A walk from the head of the log seeds
// zero; a backward page that omits it attributes everything before its
// first record to that record, which is over-attribution in the direction
// a reader cannot detect.
type Seed struct {
	Form    uint64
	Studies map[string]uint64
}

// SeedFrom reads a seed off the record preceding a window.
func SeedFrom(e store.Entry[message.Message]) Seed {
	s := Seed{Form: e.FormChannelVersion}
	if len(e.StudyVersions) > 0 {
		s.Studies = make(map[string]uint64, len(e.StudyVersions))
		for k, v := range e.StudyVersions {
			s.Studies[k] = v
		}
	}
	return s
}

// PerRecord walks the IR entries in order and returns each record's
// deltas, keyed by the record's LT and then by "<formid>.<path>".
func PerRecord(b Backend, ariaID string, entries []store.Entry[message.Message]) map[uint64]map[string]livedoc.FormDelta {
	return PerRecordFrom(b, ariaID, Seed{}, entries)
}

// PerRecordFrom is PerRecord with the cursors seeded; see Seed.
func PerRecordFrom(b Backend, ariaID string, seed Seed, entries []store.Entry[message.Message]) map[uint64]map[string]livedoc.FormDelta {
	if b == nil || ariaID == "" {
		return nil
	}
	out := map[uint64]map[string]livedoc.FormDelta{}
	lastForm := seed.Form
	lastStudy := map[string]uint64{}
	for k, v := range seed.Studies {
		lastStudy[k] = v
	}
	studied := map[string]*studiedForm{}

	for _, entry := range entries {
		if entry.Payload.Role == message.RoleGenesis {
			// Genesis is furniture: advance the cursors (the stamp is
			// still where the forms stood) and attribute nothing to it.
			lastForm = maxU64(lastForm, entry.FormChannelVersion)
			for fid, upTo := range entry.StudyVersions {
				lastStudy[fid] = maxU64(lastStudy[fid], upTo)
			}
			continue
		}

		deltas := map[string]livedoc.FormDelta{}

		// The bound board: (lastForm, entry.FormChannelVersion].
		if entry.FormChannelVersion > lastForm {
			if ps, err := b.FormPatchesBetween(ariaID, lastForm, entry.FormChannelVersion); err == nil {
				for _, vp := range ps {
					foldPatch(deltas, ariaID, livedoc.FormBound, vp.Patch)
				}
			}
			lastForm = entry.FormChannelVersion
		}

		// The observed set: the same derivation per member, from the same
		// stamp, read through the libretto -- the copy, which outlives its
		// source and is the only place a dead form's transitions exist.
		for fid, upTo := range entry.StudyVersions {
			// ADVANCE FIRST, whatever happens below.
			prev := lastStudy[fid]
			lastStudy[fid] = maxU64(prev, upTo)
			if upTo <= prev {
				continue
			}
			sf := studied[fid]
			if sf == nil {
				sf = openStudied(b, fid)
				studied[fid] = sf
			}
			if sf.lib == nil {
				continue
			}
			for _, vp := range sf.lib.PatchesBetween(prev, upTo) {
				foldStudied(deltas, fid, sf, vp.Patch)
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

// studiedForm is one observed form's read state for the walk: the
// libretto (nil when it could not be opened) and whether the copy carries
// target-aria, which is what makes it a role. The kind is decided here,
// server-side, so three clients do not each invent the predicate -- and
// it is decided ONCE per assembly, from the copy's state as it stands
// now: a form that became a role mid-history renders as a role
// throughout, which is what grouping wants, and the recast moment itself
// is still visible as the target-aria delta.
type studiedForm struct {
	lib  *store.Libretto
	role bool
}

func openStudied(b Backend, fid string) *studiedForm {
	lib, err := b.Libretto(fid)
	if err != nil || lib == nil {
		return &studiedForm{}
	}
	sf := &studiedForm{lib: lib}
	if _, ok := lib.State().Get("target-aria"); ok {
		sf.role = true
	}
	return sf
}

// foldPatch renders one bound-board patch into deltas.
func foldPatch(deltas map[string]livedoc.FormDelta, formID string, kind livedoc.FormKind, p message.Patch) {
	for k, v := range p.Set {
		deltas[formID+"."+k] = livedoc.FormDelta{
			Value: v, Kind: kind, Event: livedoc.FormSet, Form: formID,
		}
	}
	for _, k := range p.Remove {
		deltas[formID+"."+k] = livedoc.FormDelta{
			Kind: kind, Event: livedoc.FormRemoved, Form: formID,
		}
	}
}

// foldStudied renders one studied-form patch: machinery stripped except
// the death, which arrives as FormDeleted on the form itself rather than
// as a key -- a reader needs "the form is gone", not the name of the
// bookkeeping key that recorded it.
func foldStudied(deltas map[string]livedoc.FormDelta, fid string, sf *studiedForm, p message.Patch) {
	kind := livedoc.FormStudied
	if sf.role {
		kind = livedoc.FormRole
	}
	for k, v := range p.Set {
		if k == store.KeyLibrettoAlive {
			if strings.TrimSpace(string(v)) == "false" {
				deltas[fid] = livedoc.FormDelta{Kind: kind, Event: livedoc.FormDeleted, Form: fid}
			}
			continue
		}
		if store.HiddenLibrettoKey(k) {
			continue
		}
		deltas[fid+"."+k] = livedoc.FormDelta{Value: v, Kind: kind, Event: livedoc.FormSet, Form: fid}
	}
	for _, k := range p.Remove {
		if store.HiddenLibrettoKey(k) || k == store.KeyLibrettoAlive {
			continue
		}
		deltas[fid+"."+k] = livedoc.FormDelta{Kind: kind, Event: livedoc.FormRemoved, Form: fid}
	}
}

func maxU64(a, b uint64) uint64 {
	if b > a {
		return b
	}
	return a
}

// Attach folds per-record deltas onto the turns they belong to. The rule,
// chosen deliberately (plans/form-deltas-in-ui-ir.md §2): a window closes
// on the node projected from the record that carries the stamp -- for a
// tool round that is the tool node, which claims both the invoke and the
// result record. A record that projects no node (the turn's opening
// inquiry, or furniture) attaches to the TURN, exactly as Turn.At does,
// so nothing is computed and then dropped on the floor.
func Attach(turns []aria.Turn, deltas map[uint64]map[string]livedoc.FormDelta) {
	if len(deltas) == 0 {
		return
	}
	for ti := range turns {
		t := &turns[ti]
		if len(t.LTs) < 2 {
			continue
		}
		// First node to claim an LT wins: one record can project several
		// nodes (three content blocks are three nodes), and one record's
		// state must render once.
		claimed := map[uint64]int{}
		for ni := range t.Nodes {
			for _, src := range t.Nodes[ni].Src {
				if _, ok := claimed[src.LT]; !ok {
					claimed[src.LT] = ni
				}
			}
		}
		for lt := t.LTs[0]; lt <= t.LTs[1]; lt++ {
			d := deltas[lt]
			if len(d) == 0 {
				continue
			}
			if ni, ok := claimed[lt]; ok {
				t.Nodes[ni].FormDeltas = merge(t.Nodes[ni].FormDeltas, d)
			} else {
				t.FormDeltas = merge(t.FormDeltas, d)
			}
		}
	}
}

func merge(into, from map[string]livedoc.FormDelta) map[string]livedoc.FormDelta {
	if into == nil {
		into = make(map[string]livedoc.FormDelta, len(from))
	}
	for k, v := range from {
		into[k] = v
	}
	return into
}
