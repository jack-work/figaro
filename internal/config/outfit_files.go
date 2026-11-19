package config

import (
	"errors"
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
	Root     OutfitRoot
	Shadowed []OutfitSource
}

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

func OutfitFiles(roots OutfitRoots, visit func(path string)) (OutfitIndex, error) {
	idx := OutfitIndex{}
	for _, root := range roots.Dirs {
		files, err := walkOutfitRoot(root.Path, visit)
		if err != nil {
			return nil, err
		}
		for name, paths := range files {
			e := idx[name]
			if e == nil {
				idx[name] = &OutfitEntry{Paths: paths, Root: root}
				continue
			}
			for _, p := range e.Paths {
				e.Shadowed = append(e.Shadowed, OutfitSource{Path: p, Root: e.Root.Path, Managed: e.Root.Managed})
			}
			e.Paths, e.Root = paths, root
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
		idx[stem] = &OutfitEntry{Paths: []string{filepath.Join(roots.Legacy, e.Name())}, Root: OutfitRoot{Path: roots.Legacy}}
	}
	return idx, nil
}

func walkOutfitRoot(root string, visit func(path string)) (map[string][]string, error) {
	files := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root && errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipDir
			}
			return err
		}
		if path != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if visit != nil {
				visit(path)
			}
			return nil
		}
		if stem, ok := strings.CutSuffix(d.Name(), ".toml"); ok {
			files[stem] = append(files[stem], path)
		}
		return nil
	})
	return files, err
}
