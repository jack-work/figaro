package chalkboard

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

// RenderedEntry is a chalkboard entry with its body produced by a template.
type RenderedEntry struct {
	Key  string
	Body string
}

//go:embed templates
var defaultTemplates embed.FS

// LoadDefaultTemplates parses the embedded body templates.
func LoadDefaultTemplates() (*template.Template, error) {
	root := template.New("chalkboard")
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

// LoadOverrideTemplates layers user templates on top of a base set.
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

// Render produces bodies for each patch entry. Keys with a matching
// template render via that template. Keys in the harness-reserved
// `system.*` namespace are silently skipped — providers consume those
// directly. Other keys without a template fall back to a generic
// body so new chalkboard data is visible by default without needing
// a hand-rolled template per key.
func Render(p Patch, prev Snapshot, tmpls *template.Template) ([]RenderedEntry, error) {
	if p.IsEmpty() {
		return nil, nil
	}
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
		out = append(out, RenderedEntry{Key: e.Key, Body: body})
	}
	return out, nil
}

// genericBody is the fallback renderer for untemplated keys. Removal
// entries produce an empty body (caller skips). String values render
// as the bare string; object/array values render as their JSON.
func genericBody(e Entry) string {
	if e.IsRemoval() {
		return ""
	}
	if s := e.NewString(); s != "" {
		return s
	}
	return string(e.New)
}
