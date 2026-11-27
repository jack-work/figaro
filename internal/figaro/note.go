package figaro

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

const noteStampLayout = "2006-01-02T15:04:05.000Z"

const noteWriteAttempts = 4

type conditionalPrivilegedWriter interface {
	ApplyFormEffectPrivilegedIf(ariaID string, patch form.Patch, ifVersion uint64) (uint64, form.Patch, error)
}

func (a *Agent) Note(sender string, req rpc.NoteRequest) (rpc.NoteResponse, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return rpc.NoteResponse{}, errors.New("note: the text is empty")
	}
	if len(text) > rpc.NoteMaxBytes {
		return rpc.NoteResponse{}, fmt.Errorf("note: %d bytes is over the %d byte limit", len(text), rpc.NoteMaxBytes)
	}
	if a.form == nil || a.backend == nil {
		return rpc.NoteResponse{}, errors.New("note requires a form")
	}
	name := noteSenderName(sender)
	key := form.NotesRoot + "." + noteID(name, text)
	line := time.Now().UTC().Format(noteStampLayout) + " from " + name + ": " + text
	for range noteWriteAttempts {
		version, err := a.backend.FormVersion(a.id)
		if err != nil {
			return rpc.NoteResponse{}, err
		}
		snap, err := a.backend.FormState(a.id)
		if err != nil {
			return rpc.NoteResponse{}, err
		}
		patch := notePatch(snap, key, line)
		applied, err := a.writeNotePatch(patch, version)
		if errors.Is(err, store.ErrFormMoved) {
			continue
		}
		if err != nil {
			return rpc.NoteResponse{}, err
		}
		a.form.Apply(applied)
		return rpc.NoteResponse{OK: true, Key: key}, nil
	}
	return rpc.NoteResponse{}, store.ErrFormMoved
}

func (a *Agent) writeNotePatch(patch form.Patch, version uint64) (form.Patch, error) {
	if w, ok := a.backend.(conditionalPrivilegedWriter); ok {
		_, applied, err := w.ApplyFormEffectPrivilegedIf(a.id, patch, version)
		return applied, err
	}
	if _, err := a.backend.ApplyFormPrivileged(a.id, patch); err != nil {
		return form.Patch{}, err
	}
	return patch, nil
}

func noteSenderName(sender string) string {
	if name := provider.SenderName(sender); name != "" {
		return name
	}
	if sender = strings.TrimSpace(sender); sender != "" {
		return sender
	}
	return "unknown"
}

func noteID(name, text string) string {
	sum := sha256.Sum256([]byte(name + "\x00" + text))
	return hex.EncodeToString(sum[:6])
}

func notesOf(snap form.Snapshot) map[string]string {
	raw, ok := snap.Get(form.NotesRoot)
	if !ok {
		return nil
	}
	var tree map[string]json.RawMessage
	if json.Unmarshal(raw, &tree) != nil {
		return nil
	}
	out := make(map[string]string, len(tree))
	for id, v := range tree {
		var line string
		if json.Unmarshal(v, &line) == nil {
			out[form.NotesRoot+"."+id] = line
		}
	}
	return out
}

func notePatch(snap form.Snapshot, key, line string) form.Patch {
	notes := notesOf(snap)
	if notes == nil {
		notes = map[string]string{}
	}
	notes[key] = line
	keys := make([]string, 0, len(notes))
	for k := range notes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if notes[keys[i]] != notes[keys[j]] {
			return notes[keys[i]] > notes[keys[j]]
		}
		return keys[i] < keys[j]
	})
	var drop []string
	if len(keys) > rpc.NoteKeep {
		drop = keys[rpc.NoteKeep:]
	}
	value, _ := json.Marshal(line)
	return form.Build(snap, map[string]json.RawMessage{key: value}, drop)
}

func (a *Agent) rememberShownNotes() {
	if a.form == nil {
		a.notesShown = nil
		return
	}
	a.notesShown = notesOf(a.form.Snapshot())
}

func (a *Agent) clearShownNotes() {
	shown := a.notesShown
	a.notesShown = nil
	if len(shown) == 0 || a.form == nil || a.backend == nil {
		return
	}
	current := notesOf(a.form.Snapshot())
	var drop []string
	for k, line := range shown {
		if current[k] == line {
			drop = append(drop, k)
		}
	}
	if len(drop) == 0 {
		return
	}
	sort.Strings(drop)
	patch := form.Build(a.form.Snapshot(), nil, drop)
	if patch.IsIdentity() {
		return
	}
	if _, err := a.backend.ApplyFormPrivileged(a.id, patch); err != nil {
		slog.Error("clear shown notes", "aria", a.id, "err", err)
		return
	}
	a.form.Apply(patch)
}
