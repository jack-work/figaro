package store

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DefaultFormRecord is the daemon's pointer to the CURRENT default form —
// the unbound form `fig new` binds from. Successor of KeepStump: where the
// stump was content-addressed and deduped, the default form is a plain
// form plus this record of what it was born from, so the reload lifecycle
// can decide when a fresh one is due (plans/forms-and-roles-v2.md §6).
type DefaultFormRecord struct {
	FormID string `json:"form_id"`
	// BirthHash is the content version of the materialized outfit the form
	// was minted from. Compared against the outfit files ONLY when Dirty —
	// `fig outfit reload` is a cheap flag, the compute happens on the next
	// `fig new`.
	BirthHash string `json:"birth_hash"`
	// BirthVersion is the form's version at mint. If the live version has
	// moved past it, the default form was patched by hand; the next
	// reload-compute remints rather than propagating the ad-hoc patch to
	// every future aria.
	BirthVersion uint64 `json:"birth_version"`
	Dirty        bool   `json:"dirty,omitempty"`
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

// SaveDefaultForm writes the pointer durably (rename over temp).
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
