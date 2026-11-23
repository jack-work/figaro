package authz

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type fileKeyStore struct{ path string }

type storedKeys struct {
	Fetched time.Time       `json:"fetched"`
	JWKS    json.RawMessage `json:"jwks"`
}

func (f *fileKeyStore) Load() ([]byte, time.Time, bool) {
	raw, err := os.ReadFile(f.path)
	if err != nil {
		return nil, time.Time{}, false
	}
	var s storedKeys
	if json.Unmarshal(raw, &s) != nil || len(s.JWKS) == 0 {
		return nil, time.Time{}, false
	}
	return s.JWKS, s.Fetched, true
}

func (f *fileKeyStore) Save(jwks []byte, fetched time.Time) error {
	raw, err := json.Marshal(storedKeys{Fetched: fetched, JWKS: jwks})
	if err != nil {
		return err
	}
	return writePrivate(f.path, raw)
}

func writePrivate(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".issuer.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func scopeFileName(scope string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		}
		return '_'
	}, scope)
}

type fileSubjectStore struct{ path string }

func (f *fileSubjectStore) Load() ([]byte, bool) {
	raw, err := os.ReadFile(f.path)
	return raw, err == nil
}

func (f *fileSubjectStore) Save(raw []byte) error { return writePrivate(f.path, raw) }
