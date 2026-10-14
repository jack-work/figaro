package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func OutfitFiles(configDir string, visit func(dir string)) (map[string][]string, error) {
	files := map[string][]string{}
	root := filepath.Join(configDir, "outfits")
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
	if err != nil {
		return nil, err
	}
	legacy := filepath.Join(configDir, "loadouts")
	entries, err := os.ReadDir(legacy)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		stem, ok := strings.CutSuffix(e.Name(), ".toml")
		if !ok || e.IsDir() || strings.HasPrefix(e.Name(), ".") || files[stem] != nil {
			continue
		}
		files[stem] = []string{filepath.Join(legacy, e.Name())}
	}
	return files, nil
}

func outfitNames(files map[string][]string) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
