package store

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// A figaro refused over a corrupt frame used to say only "corrupt frame at
// byte N", with nothing in the log and nothing about what to do next. The
// refusal must name the channel, the segment and the frame, point at the
// release, and log once however often the figaro is asked for.
func TestARefusalOverACorruptFrameSaysWhereAndHowToRelease(t *testing.T) {
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
	id, _ := be.CreateConversation(label)
	lg, _ := be.OpenFigIR(id)
	for i := 0; i < 4; i++ {
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
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("record-1")) {
				seg = p
			}
		}
		return nil
	})
	raw, _ := os.ReadFile(seg)
	hit := bytes.Index(raw, []byte("record-1"))
	raw[hit] ^= 0x01
	if err := os.WriteFile(seg, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	at := bytes.LastIndexByte(raw[:hit], '\n') + 1

	be2, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be2.Close()
	for try := 0; try < 2; try++ {
		_, err := be2.OpenFigIR(id)
		if err == nil {
			t.Fatal("a figaro opened over a corrupt frame")
		}
		for _, want := range []string{
			seg,
			fmt.Sprintf("corrupt frame at byte %d", at),
			"figaro doctor quarantine ls",
			fmt.Sprintf("figaro doctor quarantine release %s --at %d --channel ir", id, at),
		} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the refusal does not name %q:\n%v", want, err)
			}
		}
	}
	if n := strings.Count(logged.String(), "figaro refused"); n != 1 {
		t.Fatalf("want one refusal line for two refusals, got %d:\n%s", n, logged.String())
	}
	for _, want := range []string{"channel=ir", filepath.Base(seg), fmt.Sprintf("offset=%d", at)} {
		if !strings.Contains(logged.String(), want) {
			t.Fatalf("the refusal line does not name %q:\n%s", want, logged.String())
		}
	}
	if _, err := be2.ReleaseQuarantine(id, "ir", int64(at), false); err != nil {
		t.Fatalf("the release the refusal points at is refused: %v", err)
	}
}
