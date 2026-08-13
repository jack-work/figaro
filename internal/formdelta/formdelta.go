// Package formdelta derives, for each IR record, the form state a reader
// of a transcript would have needed to understand it: what changed on the
// bound board, on every studied form, and the moment a role was recast.
//
// It is assembled FROM THE STORE, by the same cursor arithmetic the
// provider projection uses (internal/provider/projection.go), and never
// from the provider's translated bytes. That is the load-bearing
// constraint: the provider cache holds RENDERED bytes for one dialect,
// keyed by a fingerprint, so deriving UI state from it would make the UI a
// function of which provider last spoke -- and it is the layer that has
// twice made a wrong rendering permanent.
//
// Three rules inherited from the projection, each of which was a bug
// there first:
//
//   - a libretto is read through Libretto(source), never as a node: a
//     second Form over that channel replays once and never hears the fold
//     again, so its reader is orphaned at the version it opened at.
//   - system.libretto.* is stripped except `alive`: `at` moves on every
//     fold and `refs` moves when ANOTHER aria studies the same form.
//     Neither is this reader's business.
//   - the cursor ADVANCES on every stamped record, whatever else happens:
//     a cursor is where a form stood, which is true of a form that has
//     since been deleted too.
//
// Legacy "study:" stamps (source versions, from before librettos) never
// reach this package: store.Entry.StudyVersions carries only the
// "libretto:" namespace, so an old transcript simply shows no form deltas.
package formdelta

import (
	"strings"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
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

// PerRecord walks the IR entries in order and returns each record's
// deltas, keyed by the record's LT and then by "<formid>.<path>".
//
// A WINDOW CLOSES ON THE RECORD THAT CARRIES THE STAMP. For the model the
// rule was RoleInput (only a user message can carry a study block); for
// the UI IR every record projects to something a client can attach state
// to -- an input record is the turn's inquiry or a steering node, an
// output record is prose, thinking or a tool node -- so the natural
// window is (previous stamp, this stamp], attributed to this record. The
// projector decides which visible unit that LT lands on; nothing here is
// dropped on the floor, which is the failure mode this rule exists to
// prevent (see the displaced study window, plans/progress.md session 4).
//
// Assembly is best-effort per form: a libretto that cannot be opened
// contributes nothing for that form, exactly as the projection's nil
// accessor renders nothing. Determinism is a contract: the deltas are a
// pure function of durable stamps and durable patch logs, so two calls
// over the same entries MUST return equal maps, and a test holds that.
func PerRecord(b Backend, ariaID string, entries []store.Entry[message.Message]) map[uint64]map[string]livedoc.FormDelta {
	if b == nil || ariaID == "" {
		return nil
	}
	out := map[uint64]map[string]livedoc.FormDelta{}
	var lastForm uint64
	lastStudy := map[string]uint64{}
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
