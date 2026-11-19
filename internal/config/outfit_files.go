package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type OutfitRoot struct {
	Path    string
	Managed bool
}

type OutfitRoots struct {
	Dirs   []OutfitRoot
	Legacy string
}

func DefaultOutfitRoots(configDir string) OutfitRoots {
	return OutfitRoots{
		Dirs:   []OutfitRoot{{Path: filepath.Join(configDir, "outfits")}},
		Legacy: filepath.Join(configDir, "loadouts"),
	}
}

func (r OutfitRoots) Overlay() string {
	if len(r.Dirs) == 0 {
		return ""
	}
	return r.Dirs[len(r.Dirs)-1].Path
}

func (r OutfitRoots) Layered() bool { return len(r.Dirs) > 1 }

type OutfitSource struct {
	Path    string
	Root    string
	Managed bool
}

type OutfitEntry struct {
	Paths    []string
	Reads    []string
	Root     OutfitRoot
	Shadowed []OutfitSource
	LockedBy string
	Refused  []OutfitSource

	ManagedOnly OutfitView
}

type OutfitView struct {
	Paths    []string
	Reads    []string
	Root     OutfitRoot
	Shadowed []OutfitSource
}

func (e *OutfitEntry) View(managedOnly bool) OutfitView {
	if e == nil {
		return OutfitView{}
	}
	if managedOnly {
		return e.ManagedOnly
	}
	return OutfitView{Paths: e.Paths, Reads: e.Reads, Root: e.Root, Shadowed: e.Shadowed}
}

func (e *OutfitEntry) OverlaidBy() []OutfitSource {
	if e == nil || e.Root.Managed {
		return nil
	}
	var out []OutfitSource
	for _, p := range e.Paths {
		out = append(out, OutfitSource{Path: p, Root: e.Root.Path})
	}
	return out
}

type OutfitRefusal struct {
	Name string
	OutfitSource
	LockedBy string
}

const OutfitLockFile = ".locked"

type OutfitIndex map[string]*OutfitEntry

func (idx OutfitIndex) Names() []string {
	names := make([]string, 0, len(idx))
	for name, e := range idx {
		if len(e.Paths) > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (idx OutfitIndex) Refusals() []OutfitRefusal {
	var out []OutfitRefusal
	for _, name := range idx.lockedNames() {
		e := idx[name]
		for _, src := range e.Refused {
			out = append(out, OutfitRefusal{Name: name, OutfitSource: src, LockedBy: e.LockedBy})
		}
	}
	return out
}

func (idx OutfitIndex) Locked() []string {
	var names []string
	for name, e := range idx {
		if e.LockedBy != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (idx OutfitIndex) lockedNames() []string {
	var names []string
	for name, e := range idx {
		if len(e.Refused) > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func OutfitFiles(roots OutfitRoots, visit func(path string)) (OutfitIndex, error) {
	if err := roots.Distinct(); err != nil {
		return nil, err
	}
	idx := OutfitIndex{}
	for _, root := range roots.Dirs {
		files, err := walkOutfitRoot(root.Path, visit)
		if err != nil {
			return nil, err
		}
		for name, found := range files {
			paths, reads := found.paths, found.reads
			e := idx[name]
			if e == nil {
				idx[name] = &OutfitEntry{Paths: paths, Reads: reads, Root: root}
				continue
			}
			if e.LockedBy != "" {
				for _, p := range paths {
					e.Refused = append(e.Refused, OutfitSource{Path: p, Root: root.Path, Managed: root.Managed})
				}
				continue
			}
			for _, p := range e.Paths {
				e.Shadowed = append(e.Shadowed, OutfitSource{Path: p, Root: e.Root.Path, Managed: e.Root.Managed})
			}
			e.Paths, e.Reads, e.Root = paths, reads, root
		}
		if !root.Managed {
			continue
		}
		lock := filepath.Join(root.Path, OutfitLockFile)
		names, err := readOutfitLock(lock, visit)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			e := idx[name]
			if e == nil {
				e = &OutfitEntry{Root: root}
				idx[name] = e
			}
			if e.LockedBy == "" {
				e.LockedBy = lock
			}
		}
		for _, e := range idx {
			e.ManagedOnly = OutfitView{Paths: e.Paths, Reads: e.Reads, Root: e.Root, Shadowed: e.Shadowed}
		}
	}
	if roots.Legacy == "" {
		return idx, nil
	}
	entries, err := os.ReadDir(roots.Legacy)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		stem, ok := strings.CutSuffix(e.Name(), ".toml")
		if !ok || e.IsDir() || strings.HasPrefix(e.Name(), ".") || idx[stem] != nil {
			continue
		}
		path := filepath.Join(roots.Legacy, e.Name())
		idx[stem] = &OutfitEntry{Paths: []string{path}, Reads: []string{path}, Root: OutfitRoot{Path: roots.Legacy}}
	}
	return idx, nil
}

type rootFiles struct {
	paths []string
	reads []string
}

func walkOutfitRoot(root string, visit func(path string)) (map[string]*rootFiles, error) {
	files := map[string]*rootFiles{}
	resolved, err := filepath.EvalSymlinks(root)
	if errors.Is(err, fs.ErrNotExist) {
		if visit != nil {
			visit(root)
		}
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(resolved, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(resolved, path)
		if rerr != nil {
			return rerr
		}
		named := filepath.Join(root, rel)
		if path != resolved && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if visit != nil {
				visit(named)
			}
			return nil
		}
		if stem, ok := strings.CutSuffix(d.Name(), ".toml"); ok {
			f := files[stem]
			if f == nil {
				f = &rootFiles{}
				files[stem] = f
			}
			f.paths = append(f.paths, named)
			f.reads = append(f.reads, path)
		}
		return nil
	})
	return files, err
}

func readOutfitLock(path string, visit func(path string)) ([]string, error) {
	if visit != nil {
		visit(path)
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(b), "\n") {
		line, _, _ = strings.Cut(line, "#")
		names = append(names, strings.Fields(line)...)
	}
	return names, nil
}

func ResolveOutfitRoot(path string) (string, error) {
	clean := filepath.Clean(path)
	var rest []string
	for dir := clean; ; dir = filepath.Dir(dir) {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if filepath.Dir(dir) == dir {
			return clean, nil
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
	}
}

func (r OutfitRoots) Distinct() error {
	resolved := make([]string, len(r.Dirs))
	for i, root := range r.Dirs {
		p, err := ResolveOutfitRoot(root.Path)
		if err != nil {
			return fmt.Errorf("outfit root %s: %w", root.Path, err)
		}
		resolved[i] = p
		for j := 0; j < i; j++ {
			if within(resolved[j], p) || within(p, resolved[j]) {
				return fmt.Errorf("outfit roots %s and %s are the same directory or nest inside one another", r.Dirs[j].Path, root.Path)
			}
		}
	}
	return nil
}

func (r OutfitRoots) InManagedRoot(path string) (string, error) {
	p, err := ResolveOutfitRoot(path)
	if err != nil {
		return "", err
	}
	for _, root := range r.Dirs {
		if !root.Managed {
			continue
		}
		m, err := ResolveOutfitRoot(root.Path)
		if err != nil {
			return "", err
		}
		if within(m, p) {
			return root.Path, nil
		}
	}
	return "", nil
}
