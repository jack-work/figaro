package provider

// Provider-neutral rendering of studied forms' transitions as reminder texts.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// StudyReminderTexts is deterministic: its bytes land in the per-LT cache.
func StudyReminderTexts(msg message.Message, board form.Snapshot) []string {
	var out []string
	var incantation form.StudyIncantation
	limits := form.DeltaLimits{KeyBytes: -1, TotalBytes: -1}
	if msg.Study != nil || len(msg.StudyPatches) > 0 {
		incantation = form.ReadStudyIncantation(board)
		// Point-in-time limits, so a cold retranslation reproduces the bytes.
		limits = form.ReadDeltaLimits(board)
	}
	// The began mark carries the baseline state; no separate fold block is
	// emitted for the same form at the same stamp.
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

func studyBlock(name string, body any) string {
	b, err := json.Marshal(body)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("<system-reminder name=%q>\n%s\n</system-reminder>",
		escapeStudyAttr(name), b)
}

// studyFold is nil when nothing readable changed.
func studyFold(fid string, patches []message.Patch, limits form.DeltaLimits) map[string]any {
	set := map[string]json.RawMessage{}
	removed := map[string]bool{}
	changes := 0
	dead := false
	for _, p := range patches {
		if p.IsIdentity() {
			continue
		}
		changes++
		for _, ent := range p.Entries() {
			k, v := ent.Key, ent.New
			if ent.IsRemoval() {
				continue
			}
			// The one system key a reader is owed: a studied form's death is
			// reported in band.
			if k == store.KeyLibrettoAlive {
				var alive bool
				if json.Unmarshal(v, &alive) == nil && !alive {
					dead = true
				}
				continue
			}
			if strings.HasPrefix(k, "system.") {
				continue
			}
			set[k] = v
			delete(removed, k)
		}
		for _, ent := range p.Entries() {
			if !ent.IsRemoval() {
				continue
			}
			k := ent.Key
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
		body["exists"] = false
	}
	if len(set) > 0 {
		// Keys are spent in sorted order: the per-LT cache makes whichever
		// rendering ran first permanent.
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
				// A cut value is no longer valid JSON; it rides as a string.
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
