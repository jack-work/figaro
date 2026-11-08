package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// A quarantined figaro is not stuck forever: the owner can release it. The
// release names the exact frame, does nothing until confirmed, copies the
// segment aside before touching it, cuts only at the bad frame, and trims
// the derived channels back to the history that is left.
func TestReleasingAQuarantinedHistoryCutsAtTheBadFrameAndKeepsACopy(t *testing.T) {
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
	tr, err := be.OpenTranslator(id, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		e, err := lg.Append(Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("record-%d", i))},
		}})
		if err != nil {
			t.Fatal(err)
		}
		row, _ := json.Marshal(map[string]string{"role": "user", "content": fmt.Sprintf("row-%d", i)})
		if _, err := tr.Append(Entry[[]json.RawMessage]{FigaroLT: e.LT, Payload: []json.RawMessage{row}}); err != nil {
			t.Fatal(err)
		}
	}
	_ = be.Close()

	var seg string
	_ = filepath.Walk(filepath.Join(dir, "ir"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".jsonl") {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("record-3")) {
				seg = p
			}
		}
		return nil
	})
	corrupted, _ := os.ReadFile(seg)
	at := bytes.Index(corrupted, []byte("record-3"))
	frame := int64(bytes.LastIndexByte(corrupted[:at], '\n') + 1)
	corrupted[at] ^= 0x01
	if err := os.WriteFile(seg, corrupted, 0o644); err != nil {
		t.Fatal(err)
	}
	rows, _ := filepath.Glob(filepath.Join(dir, "translations-v2", "anthropic", filepath.Base(filepath.Dir(seg)), "*.jsonl"))
	rowsBefore, _ := os.ReadFile(rows[0])

	be, err = NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	if _, err := be.OpenFigIR(id); err == nil {
		t.Fatal("fixture: the corrupt history opened")
	}

	found, err := be.ScanQuarantine()
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Trunk != id || found[0].Channel != "ir" || found[0].Segment != seg || found[0].Offset != frame {
		t.Fatalf("scan: %+v; want %s ir %s at %d", found, id, seg, frame)
	}

	if _, err := be.ReleaseQuarantine(id, "", frame+1, true); !errors.Is(err, ErrNotQuarantinedAt) {
		t.Fatalf("a release at the wrong offset: %v", err)
	}
	plan, err := be.ReleaseQuarantine(id, "", frame, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applied || plan.BytesCut != int64(len(corrupted))-frame {
		t.Fatalf("plan: %+v", plan)
	}
	if now, _ := os.ReadFile(seg); !bytes.Equal(now, corrupted) {
		t.Fatal("an unconfirmed release changed the segment")
	}

	rel, err := be.ReleaseQuarantine(id, "", frame, true)
	if err != nil {
		t.Fatal(err)
	}
	aside, err := os.ReadFile(filepath.Join(rel.Aside, filepath.Base(seg)))
	if err != nil || !bytes.Equal(aside, corrupted) {
		t.Fatalf("the segment was not copied aside byte for byte: %v", err)
	}
	if now, _ := os.ReadFile(seg); !bytes.Equal(now, corrupted[:frame]) {
		t.Fatalf("the segment was not cut at the bad frame: %d bytes, want %d", len(now), frame)
	}
	if got := rel.Trimmed["translations-v2/anthropic"]; got != 3 {
		t.Fatalf("translations trimmed %d rows, want 3 (rows for records 3-5): %+v", got, rel.Trimmed)
	}
	if after, _ := os.ReadFile(rows[0]); bytes.Count(after, []byte("\n")) != bytes.Count(rowsBefore, []byte("\n"))-3 {
		t.Fatal("the translation segment does not match the trim it reported")
	}

	lg2, err := be.OpenFigIR(id)
	if err != nil {
		t.Fatalf("the released history does not open: %v", err)
	}
	var texts []string
	for _, e := range lg2.Read() {
		for _, c := range e.Payload.Content {
			texts = append(texts, c.Text)
		}
	}
	if strings.Join(texts, ",") != "record-0,record-1,record-2" {
		t.Fatalf("history after release: %v", texts)
	}
	if again, _ := be.ScanQuarantine(); len(again) != 0 {
		t.Fatalf("still quarantined after release: %+v", again)
	}
}

// The form channel's segments open with a watermark header; a release past
// it cuts like any other, and the form reads back the state before the cut.
func TestReleasingAQuarantinedFormKeepsTheStateBeforeTheBadPatch(t *testing.T) {
	dir := t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	label, _ := be.CreateOutfit("d", message.Patch{})
	id, _ := be.CreateConversation(label)
	for i := 1; i <= 6; i++ {
		v, _ := json.Marshal(fmt.Sprintf("v%d", i))
		if _, err := be.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{"note": v}, nil)); err != nil {
			t.Fatal(err)
		}
	}
	_ = be.Close()
	var seg string
	_ = filepath.Walk(filepath.Join(dir, "form"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".jsonl") {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(`"v3"`)) {
				seg = p
			}
		}
		return nil
	})
	b, _ := os.ReadFile(seg)
	b[bytes.Index(b, []byte(`"v3"`))+1] ^= 0x01
	if err := os.WriteFile(seg, b, 0o644); err != nil {
		t.Fatal(err)
	}

	be, err = NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	found, err := be.ScanQuarantine()
	if err != nil || len(found) != 1 || found[0].Channel != "form" {
		t.Fatalf("scan: %+v %v", found, err)
	}
	if _, err := be.ReleaseQuarantine(id, "", found[0].Offset, true); err != nil {
		t.Fatal(err)
	}
	snap, err := be.FormState(id)
	if err != nil {
		t.Fatalf("the released form does not open: %v", err)
	}
	if got := snap.Lookup("note"); got == nil || *got != "v2" {
		t.Fatalf("note after release = %v, want v2", got)
	}
}

// A release that cuts the history and then fails to trim a channel back to
// it has already changed the store. Its error must say the cut happened,
// which channel failed, and where the originals are.
func TestAReleaseThatCutsButCannotTrimSaysSo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only file")
	}
	dir := t.TempDir()
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	label, _ := be.CreateOutfit("d", message.Patch{})
	id, _ := be.CreateConversation(label)
	lg, _ := be.OpenFigIR(id)
	tr, _ := be.OpenTranslator(id, "anthropic")
	for i := 0; i < 5; i++ {
		e, err := lg.Append(Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("record-%d", i))},
		}})
		if err != nil {
			t.Fatal(err)
		}
		row, _ := json.Marshal(map[string]string{"role": "user", "content": fmt.Sprintf("row-%d", i)})
		if _, err := tr.Append(Entry[[]json.RawMessage]{FigaroLT: e.LT, Payload: []json.RawMessage{row}}); err != nil {
			t.Fatal(err)
		}
	}
	_ = be.Close()
	var seg string
	_ = filepath.Walk(filepath.Join(dir, "ir"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".jsonl") {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("record-2")) {
				seg = p
			}
		}
		return nil
	})
	b, _ := os.ReadFile(seg)
	b[bytes.Index(b, []byte("record-2"))] ^= 0x01
	if err := os.WriteFile(seg, b, 0o644); err != nil {
		t.Fatal(err)
	}
	rows, _ := filepath.Glob(filepath.Join(dir, "translations-v2", "anthropic", filepath.Base(filepath.Dir(seg)), "*.jsonl"))
	if err := os.Chmod(rows[0], 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(rows[0], 0o644) })

	be, err = NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	found, _ := be.ScanQuarantine()
	if len(found) != 1 {
		t.Fatalf("scan: %+v", found)
	}
	_, err = be.ReleaseQuarantine(id, "", found[0].Offset, true)
	if err == nil {
		t.Fatal("the trim cannot have succeeded on a read-only segment")
	}
	for _, want := range []string{"was cut at byte", "translations-v2/anthropic", filepath.Join(filepath.Dir(dir), "quarantine")} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error does not say %q: %v", want, err)
		}
	}
	if now, _ := os.ReadFile(seg); int64(len(now)) != found[0].Offset {
		t.Fatalf("the error claims a cut the segment does not show: %d bytes, cut at %d", len(now), found[0].Offset)
	}
}
