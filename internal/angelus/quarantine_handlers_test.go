package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// The release leaves a note beside the copy it set aside: when, by whom,
// and what was cut.
func TestAQuarantineReleaseRecordsWhoReleasedIt(t *testing.T) {
	dir := t.TempDir()
	be, err := store.NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	label, _ := be.CreateOutfit("d", message.Patch{})
	id, _ := be.CreateConversation(label)
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
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".jsonl") {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("record-1")) {
				b[bytes.Index(b, []byte("record-1"))] ^= 0x01
				_ = os.WriteFile(p, b, 0o644)
			}
		}
		return nil
	})
	be2, err := store.NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be2.Close()
	hs := NewHandlers(ServerConfig{Angelus: &Angelus{Registry: NewRegistry(), Backend: be2}, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})

	out, err := hs.Map[rpc.MethodQuarantineList](inProcess(context.Background()), nil)
	if err != nil {
		t.Fatal(err)
	}
	entries := out.(rpc.QuarantineListResponse).Entries
	if len(entries) != 1 || entries[0].Figaro != id {
		t.Fatalf("list: %+v", entries)
	}
	req, _ := json.Marshal(rpc.QuarantineReleaseRequest{Figaro: id, At: entries[0].Offset, Confirm: true})
	rel, err := hs.Map[rpc.MethodQuarantineRelease](inProcess(context.Background()), req)
	if err != nil {
		t.Fatal(err)
	}
	resp := rel.(rpc.QuarantineReleaseResponse)
	note, err := os.ReadFile(filepath.Join(resp.Aside, "RELEASED"))
	if err != nil || !bytes.Contains(note, []byte("by ")) || !bytes.Contains(note, []byte(resp.Segment)) {
		t.Fatalf("no record of the release beside the copy: %v\n%s", err, note)
	}
	if _, err := be2.OpenFigIR(id); err != nil {
		t.Fatalf("the released figaro does not open: %v", err)
	}
}

func quarantinedTranslator(t *testing.T) (dir, id string, at int64) {
	t.Helper()
	dir = t.TempDir()
	be, err := store.NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	label, _ := be.CreateOutfit("d", message.Patch{})
	id, _ = be.CreateConversation(label)
	lg, _ := be.OpenFigIR(id)
	tr, err := be.OpenTranslator(id, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		e, err := lg.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("record-%d", i))},
		}})
		if err != nil {
			t.Fatal(err)
		}
		row, _ := json.Marshal(map[string]string{"role": "user", "content": fmt.Sprintf("row-%d", i)})
		if _, err := tr.Append(store.Entry[[]json.RawMessage]{FigaroLT: e.LT, Payload: []json.RawMessage{row}}); err != nil {
			t.Fatal(err)
		}
	}
	_ = be.Close()
	segs, _ := filepath.Glob(filepath.Join(dir, "translations-v2", "anthropic", "*", "*.jsonl"))
	if len(segs) != 1 {
		t.Fatalf("fixture: translation segments %v", segs)
	}
	b, _ := os.ReadFile(segs[0])
	second := bytes.IndexByte(b, '\n') + 1
	b[second+20] ^= 0x01
	if err := os.WriteFile(segs[0], b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, id, int64(second)
}

type gatedQuarantiner struct {
	*store.XwalBackend
	inside, proceed chan struct{}
}

func (g gatedQuarantiner) ReleaseQuarantine(id, channel string, offset int64, apply bool) (store.QuarantineRelease, error) {
	if apply {
		close(g.inside)
		<-g.proceed
	}
	return g.XwalBackend.ReleaseQuarantine(id, channel, offset, apply)
}

// A quarantined translator does not stop a wake the way a quarantined
// history does, so the release itself must keep the figaro asleep: a wake
// asked for while the segment is being cut is refused, not run on a
// half-cut channel.
func TestAWakeDuringAQuarantineReleaseIsRefused(t *testing.T) {
	dir, id, at := quarantinedTranslator(t)
	be, err := store.NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	g := gatedQuarantiner{XwalBackend: be, inside: make(chan struct{}), proceed: make(chan struct{})}
	hs := NewHandlers(ServerConfig{Angelus: &Angelus{Registry: NewRegistry(), Backend: g}, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})

	req, _ := json.Marshal(rpc.QuarantineReleaseRequest{Figaro: id, Channel: "translations-v2/anthropic", At: at, Confirm: true})
	released := make(chan error, 1)
	go func() {
		_, err := hs.Map[rpc.MethodQuarantineRelease](inProcess(context.Background()), req)
		released <- err
	}()
	<-g.inside
	_, err = hs.h.restoreByID(inProcess(context.Background()), id, "test")
	close(g.proceed)
	if err == nil || !strings.Contains(err.Error(), "being released") {
		t.Fatalf("a wake during the release was not refused for it: %v", err)
	}
	if hs.h.angelus.Registry.Get(id) != nil {
		t.Fatal("the figaro woke while its segment was being cut")
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if _, err := hs.h.restoreByID(inProcess(context.Background()), id, "test"); err != nil && strings.Contains(err.Error(), "being released") {
		t.Fatalf("the release left its mark behind: %v", err)
	}
}

func TestAMissingReleasedNoteIsReported(t *testing.T) {
	dir, id, at := quarantinedTranslator(t)
	be, err := store.NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	hs := NewHandlers(ServerConfig{Angelus: &Angelus{Registry: NewRegistry(), Backend: be}, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	plan, err := be.ReleaseQuarantine(id, "", at, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(plan.Aside, "RELEASED"), 0o700); err != nil {
		t.Fatal(err)
	}
	req, _ := json.Marshal(rpc.QuarantineReleaseRequest{Figaro: id, At: at, Confirm: true})
	out, err := hs.Map[rpc.MethodQuarantineRelease](inProcess(context.Background()), req)
	if err != nil {
		t.Fatal(err)
	}
	if w := out.(rpc.QuarantineReleaseResponse).Warning; !strings.Contains(w, "RELEASED") {
		t.Fatalf("a release whose note could not be written said nothing: %q", w)
	}
}
