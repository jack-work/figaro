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

func forkOverCorruptParent(t *testing.T) (dir string, parent, child string) {
	t.Helper()
	dir = t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	label, _ := be.CreateOutfit("d", message.Patch{})
	parent, err = be.CreateConversation(label)
	if err != nil {
		t.Fatal(err)
	}
	lg, _ := be.OpenFigIR(parent)
	for i := 0; i < 5; i++ {
		if _, err := lg.Append(Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("parent-%d", i))},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	note, _ := json.Marshal("forked")
	child, _, err = be.ForkWith(parent, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{"note": note}, nil))
	if err != nil {
		t.Fatal(err)
	}
	clg, _ := be.OpenFigIR(child)
	if _, err := clg.Append(Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("child-own")},
	}}); err != nil {
		t.Fatal(err)
	}
	_ = be.Close()
	_ = filepath.Walk(filepath.Join(dir, "ir"), func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		b, _ := os.ReadFile(p)
		if i := bytes.Index(b, []byte("parent-1")); i >= 0 {
			b[i] ^= 0x01
			_ = os.WriteFile(p, b, 0o644)
		}
		return nil
	})
	return dir, parent, child
}

// A corrupt frame in a node a fork inherits from is shared history: cutting
// it would cut every figaro that reads through it, so release refuses and
// names who shares it.
func TestAReleaseRefusesACorruptSegmentAForkShares(t *testing.T) {
	dir, parent, child := forkOverCorruptParent(t)
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	found, err := be.ScanQuarantine()
	if err != nil || len(found) != 1 {
		t.Fatalf("scan: %+v %v", found, err)
	}
	for _, id := range []string{parent, child} {
		_, err := be.ReleaseQuarantine(id, "", found[0].Offset, true)
		if err == nil || !strings.Contains(err.Error(), "shares") {
			t.Fatalf("release of %s through a shared node: %v", id, err)
		}
	}
}
