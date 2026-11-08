package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// After an unclean shutdown the store trims every related channel back to
// the main channel's tail. A corrupt frame mid-segment used to read as the
// end of the channel there, so the repair cut every record after it. It
// must refuse instead and leave the bytes where they are.
func TestTheCrashRepairNeverTrimsPastACorruptFrame(t *testing.T) {
	dir := t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	label, _ := be.CreateOutfit("d", message.Patch{})
	id, err := be.CreateConversation(label)
	if err != nil {
		t.Fatal(err)
	}
	lg, _ := be.OpenFigIR(id)
	for i := 0; i < 6; i++ {
		if _, err := lg.Append(Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("m%d", i))},
		}}); err != nil {
			t.Fatal(err)
		}
		v, _ := json.Marshal(fmt.Sprintf("patch-%d", i))
		if _, err := be.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{"note": v}, nil)); err != nil {
			t.Fatal(err)
		}
	}
	_ = be.Close()

	var seg string
	_ = filepath.Walk(filepath.Join(dir, "form"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".jsonl") {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("patch-2")) {
				seg = p
			}
		}
		return nil
	})
	if seg == "" {
		t.Fatal("fixture: no form segment holds patch-2")
	}
	raw, _ := os.ReadFile(seg)
	raw[bytes.Index(raw, []byte("patch-2"))] ^= 0x01
	if err := os.WriteFile(seg, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".unclean"), []byte("open\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	be2, oerr := NewXwalBackend(dir, 0)
	if err := oerr; err == nil {
		if _, ferr := be2.FormState(id); ferr == nil {
			after, _ := os.ReadFile(seg)
			be2.Close()
			if !bytes.Equal(raw, after) {
				t.Fatalf("the repair trimmed the form channel: %d bytes before, %d after", len(raw), len(after))
			}
			t.Fatal("a corrupt form record mid-segment went unreported")
		}
		be2.Close()
	}
	after, _ := os.ReadFile(seg)
	if !bytes.Equal(raw, after) {
		t.Fatalf("the open rewrote the segment: %d bytes before, %d after", len(raw), len(after))
	}
}
