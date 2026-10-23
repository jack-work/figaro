package form

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

func osReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

type RenderedEntry struct {
	Key  string
	Body string
}

//go:embed templates
var defaultTemplates embed.FS

func LoadDefaultTemplates() (*template.Template, error) {
	root := template.New("form")
	err := fs.WalkDir(defaultTemplates, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".tmpl") {
			return nil
		}
		body, err := fs.ReadFile(defaultTemplates, path)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.Base(path), ".tmpl")
		_, err = root.New(name).Parse(string(body))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("load default templates: %w", err)
	}
	return root, nil
}

func LoadOverrideTemplates(base *template.Template, dir string) (*template.Template, error) {
	root, err := base.Clone()
	if err != nil {
		return nil, fmt.Errorf("clone base templates: %w", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.tmpl"))
	if err != nil {
		return nil, fmt.Errorf("glob overrides: %w", err)
	}
	for _, path := range matches {
		body, err := osReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		name := strings.TrimSuffix(filepath.Base(path), ".tmpl")
		if _, err := root.New(name).Parse(string(body)); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	return root, nil
}

// Render skips `system.*` keys, which providers consume directly; a key
// without a template gets a generic body.
func Render(p Patch, prev Snapshot, tmpls *template.Template) ([]RenderedEntry, error) {
	if p.IsIdentity() {
		return nil, nil
	}
	// Limits come off the same bound form as the render, so a cold retranslation
	// reproduces these bytes exactly.
	limits := ReadDeltaLimits(prev)
	entries := PatchEntries(p, prev)
	out := make([]RenderedEntry, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Key, "system.") {
			continue
		}
		t := tmpls.Lookup(e.Key)
		if t == nil {
			body := genericBody(e)
			if body == "" {
				continue
			}
			body, _ = TruncateUTF8(body, limits.KeyBytes)
			out = append(out, RenderedEntry{Key: e.Key, Body: body})
			continue
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, e); err != nil {
			return nil, fmt.Errorf("render %q: %w", e.Key, err)
		}
		body := strings.TrimSpace(buf.String())
		if body == "" {
			continue
		}
		body, _ = TruncateUTF8(body, limits.KeyBytes)
		out = append(out, RenderedEntry{Key: e.Key, Body: body})
	}
	return out, nil
}

// genericBody is empty for a removal.
func genericBody(e Entry) string {
	if e.IsRemoval() {
		return ""
	}
	if s := e.NewString(); s != "" {
		return s
	}
	return string(e.New)
}
