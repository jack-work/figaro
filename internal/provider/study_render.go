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

	"github.com/jack-work/figaro/internal/message"
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
// ONE BLOCK PER FORM, NOT PER PATCH, and it states the RESULT rather than the
// history. That rule was bought at the haiku tier, in a storm of fifty
// observers: a window carrying three patches used to render three bare
// {"set":{...}} envelopes, and a model reading two blocks that both set
// `brief` has no way to know which is current. One of them answered from the
// FIRST, reporting the value the form held before the change it was being
// asked about. An intermediate value inside one window is not information, it
// is a trap.
func StudyReminderTexts(msg message.Message) []string {
	var out []string
	if msg.Study != nil {
		out = append(out, studyBlock("study", map[string]any{
			"form": msg.Study.FormID, "observing": msg.Study.Began,
		}))
	}
	if len(msg.StudyPatches) > 0 {
		fids := make([]string, 0, len(msg.StudyPatches))
		for fid := range msg.StudyPatches {
			fids = append(fids, fid)
		}
		sort.Strings(fids)
		for _, fid := range fids {
			if body := studyFold(fid, msg.StudyPatches[fid]); body != nil {
				out = append(out, studyBlock("study:"+fid, body))
			}
		}
	}
	if len(msg.StudyNotes) > 0 {
		fids := make([]string, 0, len(msg.StudyNotes))
		for fid := range msg.StudyNotes {
			fids = append(fids, fid)
		}
		sort.Strings(fids)
		for _, fid := range fids {
			out = append(out, studyBlock("study:"+fid, map[string]any{
				"form": fid, "exists": false, "note": msg.StudyNotes[fid],
			}))
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
func studyFold(fid string, patches []message.Patch) map[string]any {
	set := map[string]json.RawMessage{}
	removed := map[string]bool{}
	changes := 0
	for _, p := range patches {
		if p.IsEmpty() {
			continue
		}
		changes++
		for k, v := range p.Set {
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
	if len(set) == 0 && len(removed) == 0 {
		return nil
	}
	body := map[string]any{"form": fid, "changes": changes}
	if len(set) > 0 {
		body["set"] = set
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
