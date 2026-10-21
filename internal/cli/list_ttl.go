package cli

import (
	"fmt"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

func reapsAt(f rpc.FigaroInfoResponse) int64 {
	if f.ReapsAt > 0 {
		return f.ReapsAt
	}
	return f.ExpiresAt
}

func expired(f rpc.FigaroInfoResponse, nowMS int64) bool {
	at := reapsAt(f)
	return at > 0 &&
		at <= nowMS &&
		f.State != "active" &&
		len(f.BoundPIDs) == 0
}

func nearReapWindow(f rpc.FigaroInfoResponse) time.Duration {
	w := 6 * time.Hour
	if ka := time.Duration(f.KeepaliveMS) * time.Millisecond; ka > 0 {
		w = min(w, ka/4)
	}
	return w
}

func lifeNote(f rpc.FigaroInfoResponse, now time.Time) string {
	var notes []string
	if f.RetiredAt > 0 {
		notes = append(notes, "retired")
	}
	if at := reapsAt(f); at > 0 {
		left := time.UnixMilli(at).Sub(now)
		switch {
		case left <= 0:
			notes = append(notes, "reaping")
		case left <= nearReapWindow(f):
			notes = append(notes, "reaps in "+shortDuration(left))
		}
	}
	if len(notes) == 0 {
		return ""
	}
	out := "["
	for i, n := range notes {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out + "]"
}

func shortDuration(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Round(time.Hour)/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Round(time.Minute)/time.Minute))
	}
	return "<1m"
}

func withLifeNote(label string, width int, f rpc.FigaroInfoResponse, now time.Time) string {
	note := lifeNote(f, now)
	if note == "" {
		return truncRunes(label, width)
	}
	room := max(width-len([]rune(note))-1, 8)
	return truncRunes(label, room) + " " + note
}

// A withheld row takes its subtree with it, except an expired ancestor of a
// surviving row, which is drawn uncounted so the survivor keeps its path to a
// root.
func dropExpired(figs []rpc.FigaroInfoResponse, nowMS int64) ([]rpc.FigaroInfoResponse, int) {
	doomed := map[string]bool{}
	for _, f := range figs {
		if expired(f, nowMS) {
			doomed[f.ID] = true
		}
	}
	if len(doomed) == 0 {
		return figs, 0
	}
	byID := make(map[string]rpc.FigaroInfoResponse, len(figs))
	for _, f := range figs {
		byID[f.ID] = f
	}
	// The seen set bounds the walk on a cycle.
	for _, f := range figs {
		if doomed[f.ID] {
			continue
		}
		seen := map[string]bool{f.ID: true}
		for id := drawnUnder(f); id != "" && !seen[id]; {
			seen[id] = true
			delete(doomed, id)
			parent, ok := byID[id]
			if !ok {
				break
			}
			id = drawnUnder(parent)
		}
	}
	if len(doomed) == 0 {
		return figs, 0
	}
	kept := make([]rpc.FigaroInfoResponse, 0, len(figs)-len(doomed))
	for _, f := range figs {
		if doomed[f.ID] {
			continue
		}
		kept = append(kept, f)
	}
	return kept, len(doomed)
}
