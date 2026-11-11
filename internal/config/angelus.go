package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type AngelusConfig struct {
	Path []string `toml:"path"`
}

func (c Config) validateAngelus() error {
	for _, dir := range c.Angelus.Path {
		if strings.ContainsRune(dir, filepath.ListSeparator) {
			return fmt.Errorf("angelus.path entry %q holds %q; give each directory as its own entry", dir, string(filepath.ListSeparator))
		}
		if !filepath.IsAbs(expandHome(dir)) {
			return fmt.Errorf("angelus.path entry %q is not absolute (~/ is allowed)", dir)
		}
	}
	return nil
}

func (l *Loaded) AngelusPath() []string {
	var out []string
	for _, dir := range l.cfg().Angelus.Path {
		out = append(out, expandHome(dir))
	}
	return out
}

func expandHome(dir string) string {
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(dir, "~"))
		}
	}
	return dir
}
