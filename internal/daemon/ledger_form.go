package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/troupe"
)

const entriesKey = "entries"

type ledgerForm struct {
	backend store.Backend
	troupe  string
	idFile  string

	mu sync.Mutex
	id string
}

func newLedgerForm(b store.Backend, d troupe.Dir, name string) *ledgerForm {
	return &ledgerForm{backend: b, troupe: name, idFile: filepath.Join(string(d), "ledger.form")}
}

func (f *ledgerForm) Load() ([]troupe.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := os.ReadFile(f.idFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, f.mint()
	}
	if err != nil {
		return nil, err
	}
	f.id = strings.TrimSpace(string(raw))
	snap, err := f.backend.FormState(f.id)
	if err != nil {
		slog.Warn("troupe ledger form is gone; minting another, peers will refill it", "form", f.id, "err", err)
		return nil, f.mint()
	}
	stored, ok := snap.Get(entriesKey)
	if !ok {
		return nil, nil
	}
	var byID map[string]string
	if err := json.Unmarshal(stored, &byID); err != nil {
		return nil, fmt.Errorf("troupe ledger form %s: %w", f.id, err)
	}
	out := make([]troupe.Entry, 0, len(byID))
	for id, s := range byID {
		var e troupe.Entry
		if err := json.Unmarshal([]byte(s), &e); err != nil {
			slog.Warn("troupe ledger form: unreadable entry dropped", "form", f.id, "entry", id, "err", err)
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (f *ledgerForm) mint() error {
	name, _ := json.Marshal(f.troupe)
	id, _, err := f.backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"troupe": name}, nil))
	if err != nil {
		return fmt.Errorf("troupe ledger form: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(f.idFile), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(f.idFile, []byte(id+"\n"), 0o600); err != nil {
		return err
	}
	f.id = id
	slog.Info("troupe ledger form minted", "troupe", f.troupe, "form", id)
	return nil
}

func (f *ledgerForm) Save(added []troupe.Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.id == "" {
		return
	}
	set := make(map[string]json.RawMessage, len(added))
	for _, e := range added {
		body, err := json.Marshal(e)
		if err != nil {
			continue
		}
		s, _ := json.Marshal(string(body))
		set[entriesKey+"."+e.ID()] = s
	}
	snap, err := f.backend.FormState(f.id)
	if err == nil {
		_, err = f.backend.ApplyFormPrivileged(f.id, form.Build(snap, set, nil))
	}
	if err != nil {
		slog.Error("troupe ledger form: entries not stored; peers still hold them", "form", f.id, "err", err)
	}
}
