package wire_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
)

func appendTurns(t *testing.T, b *store.XwalBackend, id, tag string, n int) {
	t.Helper()
	ir, err := b.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := b.OpenTranslator(id, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		e, err := ir.Append(arialog.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("%s-%d %s", tag, i, strings.Repeat("x", 512)))},
		}})
		if err != nil {
			t.Fatal(err)
		}
		row, _ := json.Marshal(map[string]string{"role": "user", "content": fmt.Sprintf("%s-row-%d", tag, i)})
		if _, err := tr.Append(arialog.Entry[[]json.RawMessage]{FigaroLT: e.LT, Payload: []json.RawMessage{row}}); err != nil {
			t.Fatal(err)
		}
	}
}

type figaroView struct {
	rows    []string
	trans   []string
	parent  string
	present string
}

func viewOf(b *store.XwalBackend, id string, translations bool) figaroView {
	var v figaroView
	for _, e := range b.OpenNode(id).Read() {
		raw, _ := json.Marshal(e.Payload)
		v.rows = append(v.rows, fmt.Sprintf("%d %s", e.LT, raw))
	}
	if translations {
		if tr, err := b.OpenTranslator(id, "anthropic"); err == nil {
			for _, e := range tr.Read() {
				raw, _ := json.Marshal(e.Payload)
				v.trans = append(v.trans, fmt.Sprintf("%d %s", e.FigaroLT, raw))
			}
		}
	}
	n, _ := b.Store().Node(id)
	v.parent, v.present = n.Parent, n.Present
	return v
}

// The canary for a half-normalized read: while normalize detaches every
// figaro drawn away from its history, readers keep reading each one's whole
// history and where it is drawn, and a writer keeps appending to the
// conversation they forked from. Every read must be the figaro before
// normalize or after it, never a mixture.
func TestNormalizeNeverShowsAReaderAHalfNormalizedFigaro(t *testing.T) {
	b, root := backend(t, true)
	stump, err := b.CreateOutfit("d", patch("system.model", "m"))
	if err != nil {
		t.Fatal(err)
	}
	conv, err := b.CreateConversation(stump)
	if err != nil {
		t.Fatal(err)
	}
	appendTurns(t, b, conv, "conv", 40)
	var alts []string
	for i := range 12 {
		_, alt, err := b.Fork(conv)
		if err != nil {
			t.Fatal(err)
		}
		appendTurns(t, b, alt, fmt.Sprintf("alt%d", i), 3)
		appendTurns(t, b, conv, fmt.Sprintf("conv-after-%d", i), 2)
		alts = append(alts, alt)
	}
	drawn := map[string]string{}
	for _, alt := range alts {
		drawn[alt] = "null"
	}
	if err := b.HierarchyEngine().WriteDrawn(drawn, nil); err != nil {
		t.Fatal(err)
	}
	if got := b.Store().Tree().Overridden(); len(got) != len(alts) {
		t.Fatalf("overridden %v, want all %d forks", got, len(alts))
	}
	want := map[string]figaroView{}
	for _, alt := range alts {
		want[alt] = viewOf(b, alt, true)
	}
	convBefore := viewOf(b, conv, false).rows

	done := make(chan struct{})
	var reads atomic.Int64
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
				}
				for _, alt := range alts {
					got, w := viewOf(b, alt, false), want[alt]
					if !slices.Equal(got.rows, w.rows) {
						t.Errorf("half-normalized read of %s: %d rows, want %d", alt, len(got.rows), len(w.rows))
						return
					}
					if got.present != w.present {
						t.Errorf("half-normalized read of %s: drawn under %q, want %q", alt, got.present, w.present)
						return
					}
					if got.parent != w.parent && got.parent != "null" {
						t.Errorf("half-normalized read of %s: history parent %q, want %q or the root", alt, got.parent, w.parent)
						return
					}
					reads.Add(1)
				}
				if rows := viewOf(b, conv, false).rows; len(rows) < len(convBefore) || !slices.Equal(rows[:len(convBefore)], convBefore) {
					t.Errorf("the conversation the forks came from lost rows mid-normalize")
					return
				}
			}
		})
	}
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			appendTurns(t, b, conv, fmt.Sprintf("live-%d", i), 1)
		}
	})
	r, err := b.Normalize(context.Background(), false)
	close(done)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Detached) != len(alts) {
		t.Fatalf("detached %v, want all %d forks", r.Detached, len(alts))
	}
	if reads.Load() == 0 {
		t.Fatal("no read overlapped normalize; the canary proved nothing")
	}
	t.Logf("%d reads overlapped normalize", reads.Load())

	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	if err := wire.Install(reopened.Store(), root, wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	for _, alt := range alts {
		got, w := viewOf(reopened, alt, true), want[alt]
		if !slices.Equal(got.rows, w.rows) || !slices.Equal(got.trans, w.trans) {
			t.Fatalf("%s after normalize and a reopen reads %d rows and %d translations, want %d and %d", alt, len(got.rows), len(got.trans), len(w.rows), len(w.trans))
		}
		if got.parent != "null" || got.present != w.present {
			t.Fatalf("%s after normalize: history %q drawn under %q, want the root drawn under %q", alt, got.parent, got.present, w.present)
		}
	}
}

func TestNormalizeStoppedMidwayResumesWhereItStopped(t *testing.T) {
	b, _ := backend(t, true)
	stump, err := b.CreateOutfit("d", patch("system.model", "m"))
	if err != nil {
		t.Fatal(err)
	}
	conv, err := b.CreateConversation(stump)
	if err != nil {
		t.Fatal(err)
	}
	appendTurns(t, b, conv, "conv", 5)
	drawn := map[string]string{}
	for range 4 {
		_, alt, err := b.Fork(conv)
		if err != nil {
			t.Fatal(err)
		}
		drawn[alt] = "null"
	}
	if err := b.HierarchyEngine().WriteDrawn(drawn, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := b.Normalize(ctx, false)
	if err == nil || len(r.Detached) != 0 || len(r.Remaining) != len(drawn) {
		t.Fatalf("a cancelled normalize: %+v, %v; want nothing detached, all %d remaining and the cancel", r, err, len(drawn))
	}
	if !b.NormalizePending(nil) {
		t.Fatal("a cancelled normalize left nothing pending")
	}
	r, err = b.Normalize(context.Background(), false)
	if err != nil || len(r.Detached) != len(drawn) {
		t.Fatalf("the resumed normalize: %+v, %v; want all %d detached", r, err, len(drawn))
	}
	if b.NormalizePending(nil) {
		t.Fatal("normalize finished and still reports work pending")
	}
}

// Defends a pass that nobody watches: one figaro that will not detach is
// reported with its error, and every figaro after it still detaches.
func TestNormalizeGoesPastAFigaroThatWillNotDetach(t *testing.T) {
	b, _ := backend(t, true)
	stump, err := b.CreateOutfit("d", patch("system.model", "m"))
	if err != nil {
		t.Fatal(err)
	}
	conv, err := b.CreateConversation(stump)
	if err != nil {
		t.Fatal(err)
	}
	appendTurns(t, b, conv, "conv", 3)
	drawn := map[string]string{}
	for range 4 {
		_, alt, err := b.Fork(conv)
		if err != nil {
			t.Fatal(err)
		}
		drawn[alt] = "null"
	}
	if err := b.HierarchyEngine().WriteDrawn(drawn, nil); err != nil {
		t.Fatal(err)
	}
	ids := slices.Sorted(maps.Keys(drawn))
	bad := ids[0]
	refused := errors.New("the carry refused")
	b.SetDetachCarry(func(carry []string) error {
		if slices.Contains(carry, bad) {
			return refused
		}
		return nil
	})
	r, err := b.Normalize(context.Background(), false)
	if !errors.Is(err, refused) {
		t.Fatalf("normalize past a refusal returned %v, want the refusal", err)
	}
	if !slices.Equal(r.Failed, []string{bad}) || !slices.Equal(r.Detached, ids[1:]) || len(r.Remaining) != 0 {
		t.Fatalf("normalize past a refusal: %+v; want %s failed and %v detached", r, bad, ids[1:])
	}
	if !b.NormalizePending(nil) {
		t.Fatal("the refused figaro no longer reads as pending")
	}
	if b.NormalizePending(func(id string) bool { return id == bad }) {
		t.Fatal("work is pending beyond the refused figaro")
	}
}
