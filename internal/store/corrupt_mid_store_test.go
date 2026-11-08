package store

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// One flipped byte in the middle of an aria's IR segment used to cost every
// record after it: the next open took the bad frame for a torn tail and
// truncated the segment there. The open must refuse instead, naming the
// segment, and the file must come through untouched.
func TestAFlippedByteInAnAriasHistoryIsNeverTrimmedAway(t *testing.T) {
	dir := t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	label, err := be.CreateOutfit("d", message.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := be.CreateConversation(label)
	if err != nil {
		t.Fatal(err)
	}
	lg, err := be.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := lg.Append(Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("record-%d", i))},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := be.Close(); err != nil {
		t.Fatal(err)
	}

	var seg string
	_ = filepath.Walk(filepath.Join(dir, "ir"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".jsonl") {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("record-2")) {
				seg = p
			}
		}
		return nil
	})
	if seg == "" {
		t.Fatal("fixture: no IR segment holds record-2")
	}
	raw, _ := os.ReadFile(seg)
	raw[bytes.Index(raw, []byte("record-2"))] ^= 0x01
	if err := os.WriteFile(seg, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	var openErr error
	be2, err := NewXwalBackend(dir, 0)
	if err != nil {
		openErr = err
	} else {
		defer be2.Close()
		lg2, err := be2.OpenFigIR(id)
		if err != nil {
			openErr = err
		} else {
			var texts []string
			for _, e := range lg2.Read() {
				for _, c := range e.Payload.Content {
					texts = append(texts, c.Text)
				}
			}
			if !slicesContain(texts, "record-5") {
				openErr = fmt.Errorf("history read without record-5: %v", texts)
			}
		}
	}
	after, _ := os.ReadFile(seg)
	if !bytes.Equal(raw, after) {
		t.Fatalf("the reopen rewrote the segment: %d bytes before, %d after", len(raw), len(after))
	}
	if openErr == nil {
		t.Fatal("a corrupt record mid-history went unreported")
	}
	if !strings.Contains(openErr.Error(), seg) && !strings.Contains(openErr.Error(), filepath.Base(seg)) {
		t.Fatalf("refused, but the error does not name the segment: %v", openErr)
	}
}

func slicesContain(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
