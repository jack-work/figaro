package recent

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
)

type Row struct {
	Name       string `json:"name"`
	ID         string `json:"id"`
	Handle     string `json:"handle,omitempty"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Cwd        string `json:"cwd,omitempty"`
	LastActive int64  `json:"last_active"`
}

const (
	KeyID     = "id"
	KeyHandle = "handle"
	KeyTitle  = "title"
	KeyState  = "state"
	KeyCwd    = "cwd"
	KeyActive = "active_at"
)

func Rows(figs []rpc.FigaroInfoResponse) []Row {
	rows := make([]Row, 0, len(figs))
	for i := range figs {
		f := &figs[i]
		if f.ReapedAt != 0 {
			continue
		}
		rows = append(rows, Row{
			ID: f.ID, Handle: f.Handle, State: f.State, Cwd: f.Cwd, LastActive: f.LastActive,
			Title: firstOf(f.Mantra, f.Handle, f.ID),
		})
	}
	slices.SortStableFunc(rows, Newer)
	name(rows)
	return rows
}

func Newer(a, b Row) int {
	switch {
	case a.LastActive > b.LastActive:
		return -1
	case a.LastActive < b.LastActive:
		return 1
	}
	return strings.Compare(a.ID, b.ID)
}

func FirstWord(handleOrID string) string {
	sigil, body := "", handleOrID
	if strings.HasPrefix(body, "@") {
		sigil, body = "@", body[1:]
	}
	word, _, _ := strings.Cut(body, handle.Separator)
	return sigil + word
}

func name(rows []Row) {
	byWord := make(map[string]int, len(rows))
	for i := range rows {
		byWord[firstWordOf(rows[i])]++
	}
	taken := make(map[string]bool, len(rows))
	for i := range rows {
		r := &rows[i]
		r.Name = firstWordOf(*r)
		if byWord[r.Name] > 1 {
			r.Name = firstOf(r.Handle, r.ID)
		}
		if taken[r.Name] {
			r.Name = r.ID
		}
		taken[r.Name] = true
	}
}

func firstWordOf(r Row) string {
	if r.Handle == "" {
		return r.ID
	}
	return FirstWord(r.Handle)
}

func Snapshot(rows []Row) form.Snapshot {
	keys := make(map[string]json.RawMessage, len(rows)*5)
	put := func(k string, v any) {
		raw, _ := json.Marshal(v)
		keys[k] = raw
	}
	for _, r := range rows {
		base := r.Name + "."
		put(base+KeyID, r.ID)
		put(base+KeyTitle, r.Title)
		put(base+KeyState, r.State)
		if r.Handle != "" {
			put(base+KeyHandle, r.Handle)
		}
		if r.LastActive > 0 {
			put(base+KeyActive, r.LastActive)
		}
		if r.Cwd != "" {
			put(base+KeyCwd, r.Cwd)
		}
	}
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, keys, nil))
}

func Decode(snap form.Snapshot) []Row {
	var rows []Row
	for n := range snap.RootNode().Children() {
		if !n.Branch() {
			continue
		}
		r := Row{Name: n.Key()}
		for leaf := range n.Children() {
			raw, err := leaf.Encode()
			if err != nil {
				continue
			}
			switch leaf.Key() {
			case KeyID:
				_ = json.Unmarshal(raw, &r.ID)
			case KeyHandle:
				_ = json.Unmarshal(raw, &r.Handle)
			case KeyTitle:
				_ = json.Unmarshal(raw, &r.Title)
			case KeyState:
				_ = json.Unmarshal(raw, &r.State)
			case KeyCwd:
				_ = json.Unmarshal(raw, &r.Cwd)
			case KeyActive:
				_ = json.Unmarshal(raw, &r.LastActive)
			}
		}
		rows = append(rows, r)
	}
	slices.SortStableFunc(rows, Newer)
	return rows
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
