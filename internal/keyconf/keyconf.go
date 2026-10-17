package keyconf

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type Layer struct {
	Modes []string          `toml:"modes"`
	Above string            `toml:"above,omitempty"`
	Bind  map[string]string `toml:"bind"`
}

type File struct {
	Bind  map[string]map[string]string `toml:"bind"`
	Layer map[string]Layer             `toml:"layer"`
}

func (f *File) Empty() bool { return len(f.Bind) == 0 && len(f.Layer) == 0 }

func Path(configDir string) string { return filepath.Join(configDir, "keys.toml") }

func Load(path string) (File, []string, error) {
	var f File
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil, nil
		}
		return f, nil, err
	}
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return f, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var warnings []string
	for _, k := range md.Undecoded() {
		warnings = append(warnings, fmt.Sprintf("%s: nothing reads %q", filepath.Base(path), k.String()))
	}
	return f, warnings, nil
}

func (f *File) Set(layer, keys, action string) {
	if f.Layer != nil {
		if l, ok := f.Layer[layer]; ok {
			if l.Bind == nil {
				l.Bind = map[string]string{}
			}
			l.Bind[keys] = action
			f.Layer[layer] = l
			return
		}
	}
	if f.Bind == nil {
		f.Bind = map[string]map[string]string{}
	}
	if f.Bind[layer] == nil {
		f.Bind[layer] = map[string]string{}
	}
	f.Bind[layer][keys] = action
}

func (f *File) Forget(layer, keys string) bool {
	if l, ok := f.Layer[layer]; ok {
		if _, had := l.Bind[keys]; had {
			delete(l.Bind, keys)
			return true
		}
	}
	if _, had := f.Bind[layer][keys]; had {
		delete(f.Bind[layer], keys)
		if len(f.Bind[layer]) == 0 {
			delete(f.Bind, layer)
		}
		return true
	}
	return false
}

func Save(path string, f File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(Render(f)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func Render(f File) string {
	var b strings.Builder
	for _, layer := range sortedKeys(f.Bind) {
		fmt.Fprintf(&b, "[bind.%s]\n", quoteKey(layer))
		writeBinds(&b, f.Bind[layer])
		b.WriteByte('\n')
	}
	for _, name := range sortedKeys(f.Layer) {
		l := f.Layer[name]
		fmt.Fprintf(&b, "[layer.%s]\n", quoteKey(name))
		quoted := make([]string, len(l.Modes))
		for i, m := range l.Modes {
			quoted[i] = fmt.Sprintf("%q", m)
		}
		fmt.Fprintf(&b, "modes = [%s]\n", strings.Join(quoted, ", "))
		if l.Above != "" {
			fmt.Fprintf(&b, "above = %q\n", l.Above)
		}
		fmt.Fprintf(&b, "\n[layer.%s.bind]\n", quoteKey(name))
		writeBinds(&b, l.Bind)
		b.WriteByte('\n')
	}
	return b.String()
}

func writeBinds(b *strings.Builder, m map[string]string) {
	for _, k := range sortedKeys(m) {
		fmt.Fprintf(b, "%s = %q\n", quoteKey(k), m[k])
	}
}

func quoteKey(k string) string {
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return fmt.Sprintf("%q", k)
		}
	}
	if k == "" {
		return `""`
	}
	return k
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
