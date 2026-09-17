// Package keyconf reads keys.toml: the client's own keybinding file.
//
// It holds no knowledge of what a chord or an action means. Spelling, modes
// and the action vocabulary belong to the TUI that owns them; this package
// turns a file into rows and says which keys nothing read.
package keyconf

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Row is one [[key]] table, in file order.
type Row struct {
	Chord  string `toml:"chord"`
	Mode   string `toml:"mode,omitempty"`
	Action string `toml:"action,omitempty"`
}

type file struct {
	Keys []Row `toml:"key"`
}

// Path is where the file lives, beside config.toml.
func Path(configDir string) string { return filepath.Join(configDir, "keys.toml") }

// Load reads the file. A missing file is not an error: it is the default map.
// Warnings name keys nothing consumed, which is the failure mode a silent
// decoder produces (a setting that stops applying and says nothing).
func Load(path string) (rows []Row, warnings []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var f file
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, k := range md.Undecoded() {
		warnings = append(warnings, fmt.Sprintf("%s: nothing reads %q", filepath.Base(path), k.String()))
	}
	return f.Keys, warnings, nil
}

// Save writes rows back, replacing the file's [[key]] list. The file holds
// nothing else today; when it does, this is the one function that has to
// learn to preserve it.
func Save(path string, rows []Row) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	enc := toml.NewEncoder(f)
	werr := enc.Encode(file{Keys: rows})
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	return os.Rename(tmp, path)
}

// Upsert replaces the row for this chord and mode, or appends it. An empty
// action is an unbind, which is a row like any other.
func Upsert(rows []Row, r Row) []Row {
	for i := range rows {
		if rows[i].Chord == r.Chord && rows[i].Mode == r.Mode {
			rows[i] = r
			return rows
		}
	}
	return append(rows, r)
}

// Remove drops every row for this chord (all modes when mode is empty).
func Remove(rows []Row, chord, mode string) []Row {
	out := rows[:0]
	for _, r := range rows {
		if r.Chord == chord && (mode == "" || r.Mode == mode) {
			continue
		}
		out = append(out, r)
	}
	return out
}
