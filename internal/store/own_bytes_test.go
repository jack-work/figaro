package store_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// du of a trunk's own directories: every channel directory named after a
// node whose .node marker says it belongs to the trunk, one level down in
// translations-v2.
func duOwn(t *testing.T, root, id string) int64 {
	t.Helper()
	var keys []string
	marks, _ := filepath.Glob(filepath.Join(root, "ir", "*", ".node"))
	for _, m := range marks {
		body, err := os.ReadFile(m)
		if err == nil && strings.Contains(string(body), "trunk="+id+"\n") {
			keys = append(keys, filepath.Base(filepath.Dir(m)))
		}
	}
	var dirs []string
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		for _, k := range keys {
			dirs = append(dirs, filepath.Join(root, e.Name(), k))
		}
		if e.Name() == "translations-v2" {
			subs, _ := os.ReadDir(filepath.Join(root, e.Name()))
			for _, s := range subs {
				for _, k := range keys {
					dirs = append(dirs, filepath.Join(root, e.Name(), s.Name(), k))
				}
			}
		}
	}
	var n int64
	for _, d := range dirs {
		files, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, f := range files {
			if info, err := f.Info(); err == nil && info.Mode().IsRegular() {
				n += info.Size()
			}
		}
	}
	return n
}

func TestOwnBytesIsDuOfTheNodesOwnDirectories(t *testing.T) {
	root := t.TempDir()
	b, err := store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	outfit, err := b.CreateOutfit("o", form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := b.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	write := func(id string, n int) {
		ir, err := b.OpenFigIR(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"anthropic", "copilot-messages"} {
			tl, err := b.OpenTranslator(id, p)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < n; i++ {
				e, err := ir.Append(store.Entry[message.Message]{Payload: message.Message{
					Role: message.RoleInput, Content: []message.Content{message.TextContent("a record worth translating")}}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tl.Append(store.Entry[[]json.RawMessage]{FigaroLT: e.LT, Fingerprint: "fp-" + p,
					Payload: []json.RawMessage{json.RawMessage(`{"content":"a translated record"}`)}}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	write(parent, 30)
	_, child, err := b.ForkAt(parent, 40)
	if err != nil {
		t.Fatal(err)
	}
	write(child, 3)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent, child} {
		want := duOwn(t, root, id)
		if want == 0 {
			t.Fatalf("%s has no bytes on disk; the fixture wrote nothing", id)
		}
		if got := b.OwnBytes(id); got != want {
			t.Errorf("OwnBytes(%s) = %d, du of its own directories = %d", id, got, want)
		}
	}
	if b.OwnBytes(child) >= b.OwnBytes(parent) {
		t.Errorf("the fork counts %d bytes, its parent %d: the shared prefix was counted", b.OwnBytes(child), b.OwnBytes(parent))
	}
}
