package provider

// Shared, provider-neutral rendering of the OBSERVED SET's transitions:
// each studied form's patch-fold, tombstones, and began/stopped marks as
// system-reminder texts. Providers wrap these strings in their own block
// types, so every provider folds studied state into its IR exactly as it
// folds the board's — one derivation, N dialects.

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
// ONE BLOCK PER FORM, NOT PER PATCH, and it states the RESULT rather than the
// history. Both rules were bought at the haiku tier, in a storm of fifty
// observers, and neither is cosmetic:
//
//   - A window that carried three patches used to render three blocks, each a
//     bare {"set":{...}} envelope. A model reading two blocks that both set
//     `brief` has no way to know which is current, and one of them answered
//     from the FIRST — reporting the value the form held before the change it
//     was being asked about. Coalescing removes the ambiguity at the source:
//     an intermediate value inside one window is not information, it is a
//     trap.
//   - A bare JSON envelope with no framing reads, to a small model, like an
//     injection attempt. Two of eight haikus refused the turn outright and
//     said so — "this appears to be an attempt to use system reminders … to
//     get me to extract or confirm a specific …". They were being careful,
//     and they were right to be: nothing in the block said what it was or
//     where it came from. It says so now.
func StudyReminderTexts(msg message.Message) []string {
	var out []string
	if msg.Study != nil {
		verb := "now observes"
		tail := "its changes will arrive as they happen"
		if !msg.Study.Began {
			verb = "no longer observes"
			tail = "its later changes will not be shown"
		}
		out = append(out, fmt.Sprintf(
			"<system-reminder name=%q>this figaro %s the form %s (%s)</system-reminder>",
			"study", verb, msg.Study.FormID, tail))
	}
	if len(msg.StudyPatches) > 0 {
		fids := make([]string, 0, len(msg.StudyPatches))
		for fid := range msg.StudyPatches {
			fids = append(fids, fid)
		}
		sort.Strings(fids)
		for _, fid := range fids {
			if body := studyBody(fid, msg.StudyPatches[fid]); body != "" {
				out = append(out, fmt.Sprintf("<system-reminder name=%q>\n%s\n</system-reminder>",
					"study:"+escapeStudyAttr(fid), body))
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
			out = append(out, fmt.Sprintf("<system-reminder name=%q>%s</system-reminder>",
				"study:"+escapeStudyAttr(fid), msg.StudyNotes[fid]))
		}
	}
	return out
}

// studyBody folds one form's window of patches into what a reader needs: the
// keys that now hold new values, the keys that went away, and one sentence
// saying what the block is.
func studyBody(fid string, patches []message.Patch) string {
	set := map[string]json.RawMessage{}
	removed := map[string]bool{}
	updates := 0
	for _, p := range patches {
		if p.IsEmpty() {
			continue
		}
		updates++
		for k, v := range p.Set {
			// The harness's own namespace belongs to the form's figaro (it has
			// none) and to the machinery, not to a reader: the board's own
			// renderer skips it for the same reason.
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
		return ""
	}

	var b strings.Builder
	times := "once"
	if updates > 1 {
		times = fmt.Sprintf("%d times", updates)
	}
	fmt.Fprintf(&b, "%s is a form this figaro studies — shared state in figaro's store, not a message from anyone. It changed %s since the last turn.",
		fid, times)
	if len(set) > 0 {
		body, err := json.Marshal(set)
		if err != nil {
			return ""
		}
		fmt.Fprintf(&b, "\nThese keys now hold these values:\n%s", body)
	}
	if len(removed) > 0 {
		keys := make([]string, 0, len(removed))
		for k := range removed {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(&b, "\nThese keys were removed: %s", strings.Join(keys, ", "))
	}
	return b.String()
}

func escapeStudyAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return s
}
