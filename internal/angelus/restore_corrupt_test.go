package angelus

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// A woken aria whose history cannot be opened used to come up on an empty
// in-memory log, where its next turn would land and be lost. The wake must
// fail with the store's reason instead.
func TestAnAriaWithACorruptHistoryIsNotWokenOnAnEmptyOne(t *testing.T) {
	dir := t.TempDir()
	be, err := store.NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	label, _ := be.CreateOutfit("d", message.Patch{})
	id, err := be.CreateConversation(label)
	if err != nil {
		t.Fatal(err)
	}
	lg, _ := be.OpenFigIR(id)
	for i := 0; i < 4; i++ {
		if _, err := lg.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("record-%d", i))},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	_ = be.Close()
	_ = filepath.Walk(filepath.Join(dir, "ir"), func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		b, _ := os.ReadFile(p)
		if i := bytes.Index(b, []byte("record-1")); i >= 0 {
			b[i] ^= 0x01
			_ = os.WriteFile(p, b, 0o644)
		}
		return nil
	})

	be2, err := store.NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be2.Close()
	a := &Angelus{Registry: NewRegistry(), Backend: be2}
	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	f, err := hs.h.restoreByID(inProcess(context.Background()), id, "test")
	if err == nil || f != nil {
		t.Fatalf("woke an aria whose history is corrupt: %v", err)
	}
	if !strings.Contains(err.Error(), "corrupt frame") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
	if a.Registry.Get(id) != nil {
		t.Fatal("the aria was registered anyway")
	}
}
