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

// LoadDefaultTemplates parses the embedded default body templates.
// Each *.tmpl file in templates/ becomes a template named by its
// basename (e.g. cwd.tmpl → "cwd"). Returns a single template set
// where Lookup(key) yields the body template for that key.
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

// LoadOverrideTemplates layers user-supplied templates on top of a
// base set. Each *.tmpl in dir overrides the same-named template in
// the base. Missing dir is not an error (users may not supply any).
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

// Render produces rendered bodies for each entry in the patch using
// the provided template set. Entries whose key has no matching template
// are silently skipped — they're chalkboard state but not surfaced to
// the model. Empty bodies (after TrimSpace) are also skipped.
func Render(p Patch, prev Snapshot, tmpls *template.Template) ([]RenderedEntry, error) {
	if p.IsEmpty() {
		return nil, nil
	}
	entries := p.Entries(prev)
	out := make([]RenderedEntry, 0, len(entries))
	for _, e := range entries {
		t := tmpls.Lookup(e.Key)
		if t == nil {
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
