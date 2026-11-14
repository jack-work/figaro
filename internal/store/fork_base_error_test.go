package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/figwal/xwal"
)

// A node with a parent whose IR .fork cannot be read used to read as base 0:
// the child then claimed its parent's history as its own. These tests defend
// the refusal: the node and its descendants refuse, nothing else changes.

type forkFamily struct {
	dir          string
	outfit       string
	p, c1, c2, g string
}

func appendFamilyRows(t *testing.T, b *XwalBackend, id string, n int) {
	t.Helper()
	ir, err := b.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := ir.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent(id)}}}); err != nil {
			t.Fatal(err)
		}
	}
	tr, err := b.OpenTranslator(id, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	tail, _ := ir.PeekTail()
	if _, err := tr.Append(Entry[[]json.RawMessage]{FigaroLT: tail.LT, Payload: []json.RawMessage{json.RawMessage(`"` + id + `"`)}}); err != nil {
		t.Fatal(err)
	}
}

func newForkFamily(t *testing.T) forkFamily {
	t.Helper()
	f := forkFamily{dir: t.TempDir()}
	b, err := NewXwalBackend(f.dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	f.outfit, _ = b.CreateOutfit("d", patchSet(map[string]string{"system.model": "m"}))
	if f.p, err = b.CreateConversation(f.outfit); err != nil {
		t.Fatal(err)
	}
	appendFamilyRows(t, b, f.p, 6)
	if _, f.c1, err = b.ForkAt(f.p, 4); err != nil {
		t.Fatal(err)
	}
	appendFamilyRows(t, b, f.c1, 3)
	if _, f.c2, err = b.ForkAt(f.p, 3); err != nil {
		t.Fatal(err)
	}
	appendFamilyRows(t, b, f.c2, 2)
	if _, f.g, err = b.ForkAt(f.c1, 5); err != nil {
		t.Fatal(err)
	}
	appendFamilyRows(t, b, f.g, 2)
	return f
}

func irForkPath(t *testing.T, root, key string) string {
	t.Helper()
	var found string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || d.Name() != key || filepath.Base(filepath.Dir(path)) != "ir" {
			return nil
		}
		found = filepath.Join(path, ".fork")
		return filepath.SkipAll
	})
	if found == "" {
		t.Fatalf("no ir directory for %s under %s", key, root)
	}
	return found
}

type familyView struct {
	reads map[string][]Entry[message.Message]
	trans map[string][]Entry[[]json.RawMessage]
	list  []NodeView
}

func viewOf(t *testing.T, b *XwalBackend, ids ...string) familyView {
	t.Helper()
	v := familyView{reads: map[string][]Entry[message.Message]{}, trans: map[string][]Entry[[]json.RawMessage]{}}
	for _, id := range ids {
		ir, err := b.OpenFigIR(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		v.reads[id] = ir.Read()
		tr, err := b.OpenTranslator(id, "anthropic")
		if err != nil {
			t.Fatalf("%s translator: %v", id, err)
		}
		v.trans[id] = tr.Read()
	}
	for _, n := range b.Conversations() {
		n.BaseErr = nil
		v.list = append(v.list, n)
	}
	return v
}

var forkCorruptions = []struct {
	name    string
	corrupt func(path string) error
}{
	{"garbage", func(p string) error { return os.WriteFile(p, []byte("garbage\n"), 0o644) }},
	{"base=x", func(p string) error { return os.WriteFile(p, []byte("base=x\n"), 0o644) }},
	{"empty", func(p string) error { return os.WriteFile(p, nil, 0o644) }},
	{"unreadable", func(p string) error { return os.Chmod(p, 0) }},
	{"deleted", os.Remove},
}

func refusesNaming(t *testing.T, what string, err error, path string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: read did not refuse", what)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("%s: refusal does not name %s: %v", what, path, err)
	}
	var fe *xwal.ForkBaseError
	if !errors.As(err, &fe) {
		t.Fatalf("%s: refusal is not a ForkBaseError: %v", what, err)
	}
}

func TestAnUnreadableForkBaseRefusesTheNodeAndItsDescendantsOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	for _, c := range forkCorruptions {
		t.Run(c.name, func(t *testing.T) {
			f := newForkFamily(t)
			b, err := NewXwalBackend(f.dir, 0)
			if err != nil {
				t.Fatal(err)
			}
			before := viewOf(t, b, f.p, f.c2)
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}

			path := irForkPath(t, f.dir, f.c1)
			if err := c.corrupt(path); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

			b, err = NewXwalBackend(f.dir, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()

			for _, id := range []string{f.c1, f.g} {
				_, err := b.Store().Lineage(id)
				refusesNaming(t, id+" lineage", err, path)
				_, err = b.Store().LineageLinks(id)
				refusesNaming(t, id+" lineage links", err, path)
				_, err = b.OpenFigIR(id)
				refusesNaming(t, id+" ir", err, path)
				_, err = b.OpenTranslator(id, "anthropic")
				refusesNaming(t, id+" translation", err, path)
			}

			after := viewOf(t, b, f.p, f.c2)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("the parent, the sibling or the listing changed:\nbefore %+v\nafter  %+v", before, after)
			}

			var listed bool
			for _, n := range b.Conversations() {
				switch n.ID {
				case f.c1:
					listed = true
					refusesNaming(t, "c1 list entry", n.BaseErr, path)
				case f.p, f.c2, f.g:
					if n.BaseErr != nil {
						t.Fatalf("%s carries %v", n.ID, n.BaseErr)
					}
				}
			}
			if !listed {
				t.Fatal("the corrupt node fell out of the listing")
			}
		})
	}
}

func TestAnOpenHandleRefusesOnceItsForkBaseIsUnreadable(t *testing.T) {
	f := newForkFamily(t)
	b, err := NewXwalBackend(f.dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ir, err := b.OpenFigIR(f.c1)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := b.OpenTranslator(f.c1, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if len(ir.Read()) == 0 || len(tr.Read()) == 0 {
		t.Fatal("the fixture child reads nothing")
	}

	path := irForkPath(t, f.dir, f.c1)
	if err := os.WriteFile(path, []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	refusesNaming(t, "ir handle", LineageErr(ir), path)
	refusesNaming(t, "translation handle", LineageErr(tr), path)
	if got := ir.Read(); len(got) != 0 {
		t.Fatalf("an open handle served %d rows past an unreadable fork base", len(got))
	}
	_, err = ir.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleInput}})
	refusesNaming(t, "append", err, path)
	_, err = b.OpenFigIR(f.c1)
	refusesNaming(t, "reopen of the held handle", err, path)
}

func TestARootAndAStumpWithoutAnIRForkStayAtBaseZero(t *testing.T) {
	f := newForkFamily(t)
	for _, key := range []string{f.outfit} {
		if err := os.Remove(irForkPath(t, f.dir, key)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	b, err := NewXwalBackend(f.dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, n := range b.Nodes() {
		if n.BaseErr != nil {
			t.Fatalf("%s (%s) carries %v", n.ID, n.Kind, n.BaseErr)
		}
	}
	refs, err := b.Store().Lineage(f.p)
	if err != nil {
		t.Fatal(err)
	}
	if refs[0].Base != 0 {
		t.Fatalf("the top of the lineage has base %d", refs[0].Base)
	}
	if _, err := b.OpenFigIR(f.p); err != nil {
		t.Fatal(err)
	}
}
