package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// The crash repair walks every leaf with keyed rows. One leaf with a corrupt
// frame must not stop the others from being repaired, nor the store from
// opening: the corrupt leaf is left byte-for-byte as it was, refused and
// logged when it is first opened, and a leaf with an ordinary incoherent tail
// is trimmed as it always was.
func TestTheCrashRepairQuarantinesOneLeafAndRepairsTheOther(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	label, _ := be.CreateOutfit("d", message.Patch{})
	leaf := func(tag string) string {
		id, err := be.CreateConversation(label)
		if err != nil {
			t.Fatal(err)
		}
		lg, _ := be.OpenFigIR(id)
		tr, err := be.OpenTranslator(id, "anthropic")
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			e, err := lg.Append(Entry[message.Message]{Payload: message.Message{
				Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("%s-msg-%d", tag, i))},
			}})
			if err != nil {
				t.Fatal(err)
			}
			row, _ := json.Marshal(map[string]string{"role": "user", "content": fmt.Sprintf("%s-row-%d", tag, i)})
			if _, err := tr.Append(Entry[[]json.RawMessage]{FigaroLT: e.LT, Payload: []json.RawMessage{row}}); err != nil {
				t.Fatal(err)
			}
			v, _ := json.Marshal(fmt.Sprintf("%s-patch-%d", tag, i))
			if _, err := be.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{"note": v}, nil)); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	corrupt, incoherent := leaf("corrupt"), leaf("incoherent")
	_ = be.Close()

	find := func(sub, needle string) string {
		var hit string
		_ = filepath.Walk(filepath.Join(dir, sub), func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".jsonl") {
				if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(needle)) {
					hit = p
				}
			}
			return nil
		})
		if hit == "" {
			t.Fatalf("fixture: no %s segment holds %s", sub, needle)
		}
		return hit
	}

	corruptForm := find("form", "corrupt-patch-2")
	raw, _ := os.ReadFile(corruptForm)
	raw[bytes.Index(raw, []byte("corrupt-patch-2"))] ^= 0x01
	if err := os.WriteFile(corruptForm, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	incoherentIR := find("ir", "incoherent-msg-4")
	ir, _ := os.ReadFile(incoherentIR)
	cut := bytes.Index(ir, []byte("incoherent-msg-3"))
	cut = bytes.LastIndexByte(ir[:cut], '\n') + 1
	if err := os.WriteFile(incoherentIR, ir[:cut], 0o644); err != nil {
		t.Fatal(err)
	}
	rowSegs, _ := filepath.Glob(filepath.Join(dir, "translations-v2", "anthropic", filepath.Base(filepath.Dir(incoherentIR)), "*.jsonl"))
	if len(rowSegs) != 1 {
		t.Fatalf("fixture: want one translation segment for the incoherent leaf, got %v", rowSegs)
	}
	incoherentRows := rowSegs[0]
	rowsBefore, _ := os.ReadFile(incoherentRows)

	if err := os.WriteFile(filepath.Join(dir, ".unclean"), []byte("open\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	be2, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatalf("one corrupt leaf kept the whole store from opening: %v", err)
	}
	defer be2.Close()

	if after, _ := os.ReadFile(corruptForm); !bytes.Equal(raw, after) {
		t.Fatalf("the corrupt leaf was touched: %d bytes before, %d after", len(raw), len(after))
	}
	after, _ := os.ReadFile(incoherentRows)
	if got, want := bytes.Count(after, []byte("\n")), bytes.Count(rowsBefore, []byte("\n"))-2; got != want {
		t.Fatalf("the incoherent leaf's translations were not trimmed to its history: %d rows after, want %d", got, want)
	}
	if _, err := be2.FormState(corrupt); err == nil {
		t.Fatal("the corrupt leaf's form opened")
	}
	if !strings.Contains(logged.String(), "figaro refused") || !strings.Contains(logged.String(), filepath.Base(corruptForm)) {
		t.Fatalf("the corrupt leaf was not logged with its segment:\n%s", logged.String())
	}
	if _, err := be2.FormState(incoherent); err != nil {
		t.Fatalf("the repaired leaf's form does not open: %v", err)
	}
}
