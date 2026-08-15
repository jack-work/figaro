package provider

// Shared, provider-neutral rendering of the OBSERVED SET's transitions:
// each studied form's patch-fold, tombstones, and began/stopped marks as
// system-reminder texts. Providers wrap these strings in their own block
// types, so every provider folds studied state into its IR exactly as it
// folds the board's: one derivation, N dialects.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// StudyReminderTexts renders a message's studied transitions as ordered
// reminder strings. Deterministic (members sorted, keys sorted by
// encoding/json) because encoded bytes land in the per-LT cache and must be
// stable across retranslations of unchanged history.
//
// THE BODY IS STRUCTURE, NOT PROSE (Gluck, 2026-08-11). A reminder states
// state; the skills are what contextualize it. So each block is one compact
// JSON object naming the form and what moved, and nothing else.
//
// Each block carries the form VERSION its window ends at, so two blocks about
// one form can be ordered by a reader without inferring anything from
// position. That is not decoration either: with fifty haiku observers, one
// answered from the earlier of two blocks about the same key.
//
// ONE BLOCK PER FORM, NOT PER PATCH, and it states the RESULT rather than the
// history. That rule was bought at the haiku tier, in a storm of fifty
// observers: a window carrying three patches used to render three bare
// {"set":{...}} envelopes, and a model reading two blocks that both set
// `brief` has no way to know which is current. One of them answered from the
// FIRST, reporting the value the form held before the change it was being
// asked about. An intermediate value inside one window is not information, it
// is a trap.
func StudyReminderTexts(msg message.Message, board form.Snapshot) []string {
	var out []string
	// The board is read for ONE thing: the incantation. Reading it costs a
	// lookup, and only when this message carries a study event at all, so an
	// aria that studies nothing pays nothing.
	var incantation form.StudyIncantation
	limits := form.DeltaLimits{KeyBytes: -1, TotalBytes: -1}
	if msg.Study != nil || len(msg.StudyPatches) > 0 {
		incantation = form.ReadStudyIncantation(board)
		// Same board, same moment: point-in-time limits, so a cold
		// retranslation reproduces these bytes (form/limits.go).
		limits = form.ReadDeltaLimits(board)
	}
	// The mark, and the BASELINE with it. At the moment observation begins,
	// the window is (0, V]: the form's whole history, which folds to its
	// STATE, not to a change. Rendering that as an ordinary transition block
	// puts two structurally identical blocks in the context whose only
	// difference is a version number, and a small model reads the first. So
	// the mark carries the state it began with, labelled as such, and no
	// separate fold block is emitted for the same form at the same stamp.
	begun := ""
	if msg.Study != nil {
		body := map[string]any{"form": msg.Study.FormID, "observing": msg.Study.Began}
		if say := incantation.OnStudy; say != "" && msg.Study.Began {
			body["say"] = say
		}
		if say := incantation.OnDrop; say != "" && !msg.Study.Began {
			body["say"] = say
		}
		if msg.Study.Began {
			if v, ok := msg.StudyAt[msg.Study.FormID]; ok {
				body["version"] = v
			}
			if fold := studyFold(msg.Study.FormID, msg.StudyPatches[msg.Study.FormID], limits); fold != nil {
				if set, ok := fold["set"]; ok {
					body["state"] = set
				}
				if rm, ok := fold["removed"]; ok {
					body["removed"] = rm
				}
				begun = msg.Study.FormID
			}
		}
		out = append(out, studyBlock("study", body))
	}
	if len(msg.StudyPatches) > 0 {
		fids := make([]string, 0, len(msg.StudyPatches))
		for fid := range msg.StudyPatches {
			fids = append(fids, fid)
		}
		sort.Strings(fids)
		for _, fid := range fids {
			if fid == begun {
				continue // its state rode the mark
			}
			if body := studyFold(fid, msg.StudyPatches[fid], limits); body != nil {
				if v, ok := msg.StudyAt[fid]; ok {
					body["version"] = v
				}
				if incantation.OnUpdate != "" {
					body["say"] = incantation.OnUpdate
				}
				out = append(out, studyBlock("study:"+fid, body))
			}
		}
	}
	return out
}

// studyBlock wraps one body in the reminder envelope every provider uses.
func studyBlock(name string, body any) string {
	b, err := json.Marshal(body)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("<system-reminder name=%q>\n%s\n</system-reminder>",
		escapeStudyAttr(name), b)
}

// studyFold folds one form's window of patches into what a reader needs: the
// keys that now hold new values, the keys that went away, and how many times
// it moved. nil when nothing readable changed.
func studyFold(fid string, patches []message.Patch, limits form.DeltaLimits) map[string]any {
	set := map[string]json.RawMessage{}
	removed := map[string]bool{}
	changes := 0
	dead := false
	for _, p := range patches {
		if p.IsEmpty() {
			continue
		}
		changes++
		for k, v := range p.Set {
			// THE ONE SYSTEM KEY A READER IS OWED. A studied form's death is
			// reported IN BAND, as a key on the copy that outlives it
			// (durable-forms §12.7b), which is what keeps liveness out of the
			// projection entirely. It arrives here as
			// system.libretto.alive=false and would otherwise be dropped with
			// the rest of the machinery -- so the ruling would be built,
			// durable, correct, and invisible.
			if k == store.KeyLibrettoAlive {
				var alive bool
				if json.Unmarshal(v, &alive) == nil && !alive {
					dead = true
				}
				continue
			}
			// The harness's own namespace belongs to the machinery, not to a
			// reader: the board's own renderer skips it for the same reason.
			if strings.HasPrefix(k, "system.") {
				continue
			}
			set[k] = v
			delete(removed, k)
		}
		for _, k := range p.Remove {
			if strings.HasPrefix(k, "system.") {
				continue
			}
			removed[k] = true
			delete(set, k)
		}
	}
	if len(set) == 0 && len(removed) == 0 && !dead {
		return nil
	}
	body := map[string]any{"form": fid, "changes": changes}
	if dead {
		// The same word the old tombstone note used, without the error
		// framing: the form is gone, the copy is not, and history renders.
		body["exists"] = false
	}
	if len(set) > 0 {
		// BOUNDED, DETERMINISTICALLY. Keys are spent in sorted order so
		// two renderings of one record agree byte for byte -- the
		// per-LT cache makes whichever ran first permanent, and a fold
		// that shuffled would make history disagree with itself.
		names := make([]string, 0, len(set))
		for k := range set {
			names = append(names, k)
		}
		sort.Strings(names)
		shown := map[string]json.RawMessage{}
		spent, elided := 0, 0
		for _, k := range names {
			v := set[k]
			cut, wasCut := form.TruncateUTF8(string(v), limits.KeyBytes)
			if wasCut {
				// A cut value is no longer valid JSON, so it rides as a
				// JSON STRING: the block must stay parseable.
				if q, err := json.Marshal(cut); err == nil {
					v = q
				}
			}
			if limits.TotalBytes > 0 && spent > 0 && spent+len(v) > limits.TotalBytes {
				elided++
				continue
			}
			shown[k] = v
			spent += len(v)
		}
		body["set"] = shown
		if note := form.EllipsisNote(fid, elided); note != "" {
			body["elided"] = note
		}
	}
	if len(removed) > 0 {
		keys := make([]string, 0, len(removed))
		for k := range removed {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		body["removed"] = keys
	}
	return body
}

func escapeStudyAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return s
}
