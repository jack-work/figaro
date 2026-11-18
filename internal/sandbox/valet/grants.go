package valet

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	Anyone          = "*"
	DefaultMaxGrant = 7 * 24 * time.Hour
	minFigaroPrefix = 8
	grantsFile      = "grants.toml"
)

type Grant struct {
	Figaro  string    `toml:"figaro" json:"figaro"`
	Path    string    `toml:"path" json:"path"`
	Mode    string    `toml:"mode" json:"mode"`
	Granted time.Time `toml:"granted" json:"granted"`
	Expires time.Time `toml:"expires" json:"expires"`
}

func (g Grant) Write() bool { return g.Mode == "rw" }

func (g Grant) Until(max time.Duration) time.Time {
	if limit := g.Granted.Add(max); limit.Before(g.Expires) {
		return limit
	}
	return g.Expires
}

func (g Grant) Names(figaro string) bool {
	return g.Figaro == Anyone || (len(g.Figaro) >= minFigaroPrefix && strings.HasPrefix(figaro, g.Figaro))
}

type Table struct {
	Grants []Grant `toml:"grant"`
}

func StateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "figaro-valet")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "figaro-valet")
}

func GrantsPath(state string) string { return filepath.Join(state, grantsFile) }

func Load(state string) (Table, error) {
	var t Table
	data, err := os.ReadFile(GrantsPath(state))
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	if _, err := toml.Decode(string(data), &t); err != nil {
		return t, fmt.Errorf("%s: %w", GrantsPath(state), err)
	}
	return t, nil
}

func (t Table) Save(state string) error {
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(t); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(state, grantsFile+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), GrantsPath(state))
}

func (t Table) For(figaro string, now time.Time, max time.Duration) []Grant {
	var out []Grant
	for _, g := range t.Grants {
		if g.Names(figaro) && now.Before(g.Until(max)) {
			out = append(out, g)
		}
	}
	return out
}

func (t *Table) Add(g Grant) {
	t.Grants = slices.DeleteFunc(t.Grants, func(o Grant) bool { return o.Figaro == g.Figaro && o.Path == g.Path })
	t.Grants = append(t.Grants, g)
}

func (t *Table) Revoke(figaro, path string) int {
	n := len(t.Grants)
	t.Grants = slices.DeleteFunc(t.Grants, func(g Grant) bool {
		return g.Figaro == figaro && (path == "" || g.Path == path)
	})
	return n - len(t.Grants)
}

func (t *Table) Prune(now time.Time, max time.Duration) int {
	n := len(t.Grants)
	t.Grants = slices.DeleteFunc(t.Grants, func(g Grant) bool { return !now.Before(g.Until(max)) })
	return n - len(t.Grants)
}

type Request struct {
	Figaro  string
	Path    string
	Mode    string
	For     time.Duration
	Max     time.Duration
	Home    string
	Guarded []string
}

func (r Request) Grant(now time.Time) (Grant, error) {
	if r.Figaro != Anyone && (len(r.Figaro) < minFigaroPrefix || strings.Contains(r.Figaro, Anyone)) {
		return Grant{}, fmt.Errorf("name a figaro by at least %d characters of its id, or %q for every figaro", minFigaroPrefix, Anyone)
	}
	if r.Mode != "ro" && r.Mode != "rw" {
		return Grant{}, fmt.Errorf("mode is ro or rw, not %q", r.Mode)
	}
	if r.For <= 0 {
		return Grant{}, fmt.Errorf("a grant needs a positive duration, not %s", r.For)
	}
	if r.For > r.Max {
		return Grant{}, fmt.Errorf("%s is longer than the longest grant, %s", r.For, r.Max)
	}
	path, err := Canonical(r.Path)
	if err != nil {
		return Grant{}, err
	}
	if path == "/" || within(r.Home, path) {
		return Grant{}, fmt.Errorf("%s holds the home directory; grant a working tree, not a home", path)
	}
	for _, g := range r.Guarded {
		if g != "" && (within(path, g) || within(g, path)) {
			return Grant{}, fmt.Errorf("%s overlaps %s, which the valet keeps from every tool", path, g)
		}
	}
	return Grant{Figaro: r.Figaro, Path: path, Mode: r.Mode, Granted: now.UTC(), Expires: now.Add(r.For).UTC()}, nil
}

func Canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func within(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func ParseDuration(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	var mult time.Duration
	switch {
	case strings.HasSuffix(s, "d"):
		mult = 24 * time.Hour
	case strings.HasSuffix(s, "w"):
		mult = 7 * 24 * time.Hour
	}
	if mult > 0 {
		n, err := strconv.ParseFloat(strings.TrimSpace(s[:len(s)-1]), 64)
		if err != nil {
			return 0, fmt.Errorf("duration %q: %w", raw, err)
		}
		return time.Duration(n * float64(mult)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("duration %q: %w", raw, err)
	}
	return d, nil
}
