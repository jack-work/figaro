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

	"github.com/jack-work/figaro/api/message"
)

// The crash repair reads only the tail frames it judges, so a corrupt frame
// deeper in a segment is no longer met at boot. It must be met at the
// figaro's first open instead: the figaro refused, never loaded short, with
// the channel, the segment and the frame's byte offset named, the way to
// release it pointed at, and one log line. A leaf with no keyed rows, which
// the repair now never reads at all, is caught the same way.
func TestACorruptFrameBelowTheRepairedTailRefusesTheFigaroAtFirstOpen(t *testing.T) {
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
	conversation := func(tag string, rows bool) string {
		id, err := be.CreateConversation(label)
		if err != nil {
			t.Fatal(err)
		}
		lg, _ := be.OpenFigIR(id)
		var tr Log[[]json.RawMessage]
		if rows {
			if tr, err = be.OpenTranslator(id, "anthropic"); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 6; i++ {
			e, err := lg.Append(Entry[message.Message]{Payload: message.Message{
				Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("%s-msg-%d", tag, i))},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if rows {
				row, _ := json.Marshal(map[string]string{"role": "user", "content": fmt.Sprintf("%s-row-%d", tag, i)})
				if _, err := tr.Append(Entry[[]json.RawMessage]{FigaroLT: e.LT, Payload: []json.RawMessage{row}}); err != nil {
					t.Fatal(err)
				}
			}
		}
		return id
	}
	keyed, bare := conversation("keyed", true), conversation("bare", false)
	_ = be.Close()

	find := func(needle string) string {
		var hit string
		_ = filepath.Walk(filepath.Join(dir, "ir"), func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".jsonl") {
				if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(needle)) {
					hit = p
				}
			}
			return nil
		})
		if hit == "" {
			t.Fatalf("fixture: no ir segment holds %s", needle)
		}
		return hit
	}
	corruptDeep := func(seg, needle string) (int64, []byte) {
		raw, _ := os.ReadFile(seg)
		at := bytes.Index(raw, []byte(needle))
		raw[at] ^= 0x01
		if err := os.WriteFile(seg, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return int64(bytes.LastIndexByte(raw[:at], '\n') + 1), raw
	}

	keyedIR := find("keyed-msg-5")
	ir, _ := os.ReadFile(keyedIR)
	cut := bytes.LastIndexByte(ir[:bytes.Index(ir, []byte("keyed-msg-5"))], '\n') + 1
	if err := os.WriteFile(keyedIR, ir[:cut], 0o644); err != nil {
		t.Fatal(err)
	}
	keyedAt, keyedRaw := corruptDeep(keyedIR, "keyed-msg-1")
	bareIR := find("bare-msg-5")
	bareAt, bareRaw := corruptDeep(bareIR, "bare-msg-1")

	if err := os.WriteFile(filepath.Join(dir, ".unclean"), []byte("open\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	be2, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatalf("a corrupt frame below the tail kept the store from opening: %v", err)
	}
	defer be2.Close()
	if strings.Contains(logged.String(), "figaro refused") {
		t.Fatalf("boot already reported what only the first open should meet:\n%s", logged.String())
	}

	for _, c := range []struct {
		id, seg string
		at      int64
		raw     []byte
	}{{keyed, keyedIR, keyedAt, keyedRaw}, {bare, bareIR, bareAt, bareRaw}} {
		lg, err := be2.OpenFigIR(c.id)
		if err == nil {
			t.Fatalf("%s opened over a corrupt frame, reading %d records", c.id, len(lg.Read()))
		}
		msg := err.Error()
		for _, want := range []string{
			c.seg,
			fmt.Sprintf("corrupt frame at byte %d", c.at),
			fmt.Sprintf("figaro doctor quarantine release %s --at %d --channel ir", c.id, c.at),
			"figaro doctor quarantine ls",
		} {
			if !strings.Contains(msg, want) {
				t.Fatalf("the refusal of %s does not name %q:\n%s", c.id, want, msg)
			}
		}
		line := ""
		for _, l := range strings.Split(logged.String(), "\n") {
			if strings.Contains(l, "figaro refused") && strings.Contains(l, c.id) {
				line = l
			}
		}
		if line == "" || !strings.Contains(line, "channel=ir") || !strings.Contains(line, filepath.Base(c.seg)) || !strings.Contains(line, fmt.Sprintf("offset=%d", c.at)) {
			t.Fatalf("no log line names %s's channel, segment and frame:\n%s", c.id, logged.String())
		}
		if after, _ := os.ReadFile(c.seg); !bytes.Equal(after, c.raw) {
			t.Fatalf("%s's corrupt segment was rewritten: %d bytes before, %d after", c.id, len(c.raw), len(after))
		}
		if _, err := be2.OpenFigIR(c.id); err == nil {
			t.Fatalf("%s opened on the second try", c.id)
		}
		if _, err := be2.ReleaseQuarantine(c.id, "ir", c.at, false); err != nil {
			t.Fatalf("the release the refusal points at is refused: %v", err)
		}
	}
	if !strings.Contains(logged.String(), "trimmed incoherent channel tail") {
		t.Fatalf("the repair did not trim the row past the keyed leaf's tail before its first open:\n%s", logged.String())
	}
	if n := strings.Count(logged.String(), "figaro refused"); n != 2 {
		t.Fatalf("want one refusal line per figaro, got %d:\n%s", n, logged.String())
	}

	found, err := be2.ScanQuarantine()
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]int64{}
	for _, q := range found {
		listed[q.Trunk] = q.Offset
	}
	if listed[keyed] != keyedAt || listed[bare] != bareAt || len(found) != 2 {
		t.Fatalf("doctor quarantine ls does not list both: %+v", found)
	}
}
