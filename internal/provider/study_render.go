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
// reminder strings. Deterministic (members sorted) because encoded bytes
// land in the per-LT cache and must be stable across retranslations of
// unchanged history.
func StudyReminderTexts(msg message.Message) []string {
	var out []string
	if msg.Study != nil {
		verb := "began observing"
		if !msg.Study.Began {
			verb = "stopped observing"
		}
		out = append(out, fmt.Sprintf("<system-reminder name=%q>this figaro %s form %s</system-reminder>",
			"study", verb, msg.Study.FormID))
	}
	if len(msg.StudyPatches) > 0 {
		fids := make([]string, 0, len(msg.StudyPatches))
		for fid := range msg.StudyPatches {
			fids = append(fids, fid)
		}
		sort.Strings(fids)
		for _, fid := range fids {
			for _, p := range msg.StudyPatches[fid] {
				body, err := json.Marshal(p)
				if err != nil {
					continue
				}
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

func escapeStudyAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return s
}
