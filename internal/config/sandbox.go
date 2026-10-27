package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type SandboxConfig struct {
	WorkspaceRoot string               `toml:"workspace_root"`
	Boxes         map[string]BoxConfig `toml:"boxes"`
}

type BoxConfig struct {
	Exec    string `toml:"exec"`
	Door    string `toml:"door"`
	Profile string `toml:"profile"`
}

func (l *Loaded) WorkspaceRoot() (string, error) {
	root := l.cfg().Sandbox.WorkspaceRoot
	if root == "" {
		return "", nil
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("sandbox.workspace_root %q: want an absolute path", root)
	}
	return filepath.Clean(root), nil
}

func (l *Loaded) Box(name string) (BoxConfig, bool) {
	b, ok := l.cfg().Sandbox.Boxes[name]
	return b, ok
}

func (l *Loaded) BoxDoors() map[string]string {
	out := map[string]string{}
	for name, b := range l.cfg().Sandbox.Boxes {
		if b.Door != "" {
			out[name] = filepath.Clean(b.Door)
		}
	}
	return out
}

func (l *Loaded) DooredBoxes() []string {
	return sortedKeys(l.BoxDoors())
}

func (c Config) validateSandbox() error {
	doors := map[string]string{}
	for _, name := range sortedKeys(c.Sandbox.Boxes) {
		b := c.Sandbox.Boxes[name]
		where := "sandbox.boxes." + name
		switch {
		case name == "local" || name == "bwrap" || name == "self":
			return fmt.Errorf("%s: %s is reserved", where, name)
		case name == "" || strings.ContainsAny(name, ":/@* \t"):
			return fmt.Errorf("%s: a box name has no : / @ * or space", where)
		case !filepath.IsAbs(b.Exec):
			return fmt.Errorf("%s.exec: want the absolute path of the socket box serve listens on", where)
		case b.Door != "" && !filepath.IsAbs(b.Door):
			return fmt.Errorf("%s.door: want an absolute socket path", where)
		}
		if b.Door != "" {
			door := filepath.Clean(b.Door)
			if other, ok := doors[door]; ok {
				return fmt.Errorf("%s.door: %s is also box %s's door", where, door, other)
			}
			doors[door] = name
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
