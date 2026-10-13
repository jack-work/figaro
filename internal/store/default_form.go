package store

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DefaultFormRecord points at the unbound form `fig new` binds from, with what
// it was born from so the reload lifecycle can decide when a fresh one is due.
type DefaultFormRecord struct {
	FormID string `json:"form_id"`
	// BirthHash is compared against the outfit files only when Dirty: reload
	// sets the flag, the compute happens on the next `fig new`.
	BirthHash string `json:"birth_hash"`
	// BirthVersion: a live version past it means the form was patched by hand;
	// the next reload remints rather than propagating the patch to every aria.
	BirthVersion uint64 `json:"birth_version"`
	Dirty        bool   `json:"dirty,omitempty"`
	// BundledRoot is the upgrade trigger: a clean pointer is reused without any
	// comparison, so a binary upgrade replacing the shipped skills must be seen here.
	BundledRoot string `json:"bundled_root,omitempty"`
}

func (b *XwalBackend) defaultFormPath() string {
	return filepath.Join(b.root, "default_form.json")
}

// LoadDefaultForm reads the pointer; (nil, nil) when none exists yet.
func (b *XwalBackend) LoadDefaultForm() (*DefaultFormRecord, error) {
	data, err := os.ReadFile(b.defaultFormPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec DefaultFormRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (b *XwalBackend) SaveDefaultForm(rec *DefaultFormRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := b.defaultFormPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, b.defaultFormPath())
}
