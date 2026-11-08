package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// Only a corrupt frame used to refuse a figaro. Any other failure to open a
// channel (a segment it may not read, a node that will not open) read as an
// empty channel, so the figaro came up with no history and no error.
func TestAHistoryThatCannotBeReadRefusesTheFigaro(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0 file")
	}
	dir := t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	label, _ := be.CreateOutfit("d", message.Patch{})
	id, _ := be.CreateConversation(label)
	lg, _ := be.OpenFigIR(id)
	for i := 0; i < 3; i++ {
		if _, err := lg.Append(Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("record-%d", i))},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	_ = be.Close()
	var seg string
	_ = filepath.Walk(filepath.Join(dir, "ir"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".jsonl") {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "record-1") {
				seg = p
			}
		}
		return nil
	})
	if err := os.Chmod(seg, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(seg, 0o644) })

	be2, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be2.Close()
	lg2, err := be2.OpenFigIR(id)
	if err == nil {
		t.Fatalf("a history whose segment cannot be read opened, reading %d records", len(lg2.Read()))
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
}
