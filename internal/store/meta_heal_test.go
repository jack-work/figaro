package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/tokens"
)

// healFixture builds an aria with n appended messages, alternating a prose
// input and an output carrying usage, and returns the backend, the aria id and
// the LTs of the appended entries.
func healFixture(t *testing.T, dir string, n int) (*XwalBackend, string, []uint64) {
	t.Helper()
	b, err := NewXwalBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	l, err := b.CreateLoadout("default", patchSet(map[string]string{"system.credo": "be terse"}))
	if err != nil {
		t.Fatal(err)
	}
	conv, err := b.CreateConversation(l)
	if err != nil {
		t.Fatal(err)
	}
	ir, err := b.Open(conv)
	if err != nil {
		t.Fatal(err)
	}
	var lts []uint64
	for i := 1; i <= n; i++ {
		m := message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("hello")}}
		if i%2 == 0 {
			m = message.Message{
				Role:    message.RoleOutput,
				Content: []message.Content{message.TextContent("hi")},
				Usage:   &message.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 5, CacheWriteTokens: 1},
			}
		}
		e, err := ir.Append(Entry[message.Message]{Payload: m})
		if err != nil {
			t.Fatal(err)
		}
		lts = append(lts, e.LT)
	}
	return b, conv, lts
}

// TestMetaHealsOnReadBounded is the whole contract of meta_heal.go: a sidecar
// whose watermark trails is caught up by the next READ of the aria, and the
// replay touches EXACTLY the entries after the watermark — the count is
// measured, not asserted by inspection.
func TestMetaHealsOnReadBounded(t *testing.T) {
	b, conv, lts := healFixture(t, t.TempDir(), 10)

	// Sidecar checkpointed at the 4th appended entry, as an agent would have
	// written it there: 4 messages, 2 turns, 2 usage-bearing outputs.
	stale := &AriaMeta{
		MessageCount: 4, TurnCount: 2,
		TokensIn: 200, TokensOut: 20, CacheReadTokens: 10, CacheWriteTokens: 2,
		LastFigaroLT: lts[3], Mantra: "keep me", Model: "keep-me-too",
	}
	if err := b.SetMeta(conv, stale); err != nil {
		t.Fatal(err)
	}

	before := MetaHealFolded()
	if _, err := b.Open(conv); err != nil { // the read path
		t.Fatal(err)
	}
	folded := MetaHealFolded() - before
	if folded != 6 {
		t.Fatalf("folded %d entries, want exactly the 6 after the watermark", folded)
	}

	got, err := b.Meta(conv)
	if err != nil {
		t.Fatal(err)
	}
	want := AriaMeta{
		MessageCount: 10, TurnCount: 5,
		TokensIn: 500, TokensOut: 50, CacheReadTokens: 25, CacheWriteTokens: 5,
		LastFigaroLT: lts[9], Mantra: "keep me", Model: "keep-me-too",
		ContextTokens: got.ContextTokens, ContextExact: got.ContextExact,
	}
	if *got != want {
		t.Fatalf("healed meta =\n%+v\nwant\n%+v", *got, want)
	}
	// The tail is an output with usage, so context is exact and equal to that
	// turn's usage-derived figure.
	wantCtx := tokens.ContextFromUsage(&message.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 5, CacheWriteTokens: 1})
	if !got.ContextExact || got.ContextTokens != wantCtx {
		t.Fatalf("context = (%d, exact=%v), want (%d, true)", got.ContextTokens, got.ContextExact, wantCtx)
	}

	// Healing is durable: it rewrote the sidecar, not just the cache.
	onDisk, err := readJSON[AriaMeta](b.metaPath(conv))
	if err != nil || onDisk == nil || onDisk.LastFigaroLT != lts[9] || onDisk.MessageCount != 10 {
		t.Fatalf("sidecar on disk = %+v (err %v)", onDisk, err)
	}
}

// TestMetaHealNoopWhenCurrent: the settled case (212 of 213 arias in the
// author's store) must fold nothing and rewrite nothing.
func TestMetaHealNoopWhenCurrent(t *testing.T) {
	b, conv, lts := healFixture(t, t.TempDir(), 6)
	current := &AriaMeta{MessageCount: 6, TurnCount: 3, LastFigaroLT: lts[5]}
	if err := b.SetMeta(conv, current); err != nil {
		t.Fatal(err)
	}
	path := b.metaPath(conv)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	before := MetaHealFolded()
	if _, err := b.Open(conv); err != nil {
		t.Fatal(err)
	}
	if folded := MetaHealFolded() - before; folded != 0 {
		t.Fatalf("folded %d entries on an up-to-date sidecar, want 0", folded)
	}
	st2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.ModTime().Equal(st.ModTime()) {
		t.Fatal("sidecar was rewritten for an up-to-date meta")
	}

	// A watermarkless (pre-watermark) sidecar is left alone too: its counts
	// are absolute, so a suffix fold would double them.
	if err := b.SetMeta(conv, &AriaMeta{MessageCount: 6}); err != nil {
		t.Fatal(err)
	}
	before = MetaHealFolded()
	if _, err := b.Open(conv); err != nil {
		t.Fatal(err)
	}
	if folded := MetaHealFolded() - before; folded != 0 {
		t.Fatalf("folded %d entries with no watermark, want 0", folded)
	}
}

// TestSegmentRollsAtSegmentSize pins the figaro-chosen segment size: writing
// past it must produce a second segment file (with 64MB it never did).
func TestSegmentRollsAtSegmentSize(t *testing.T) {
	dir := t.TempDir()
	b, conv, _ := healFixture(t, dir, 0)
	ir, err := b.Open(conv)
	if err != nil {
		t.Fatal(err)
	}
	blob := strings.Repeat("x", 64*1024)
	for written := 0; written < 3*segmentSize/2; written += len(blob) {
		if _, err := ir.Append(Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(blob)},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.store.trunks.Close(); err != nil { // flush to disk
		t.Fatal(err)
	}
	// The aria's segments live in the leaf dir carrying the .trunk marker.
	var leaf string
	filepath.WalkDir(filepath.Join(dir, chanIR), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == ".trunk" {
			leaf = filepath.Dir(p)
		}
		return nil
	})
	segs, _ := filepath.Glob(filepath.Join(leaf, "*.jsonl"))
	if len(segs) < 2 {
		t.Fatalf("wrote 1.5x segmentSize (%d bytes) into %d segment(s): %v", segmentSize, len(segs), segs)
	}
	for _, p := range segs[:len(segs)-1] {
		st, _ := os.Stat(p)
		if st.Size() > segmentSize {
			t.Fatalf("segment %s is %d bytes, over segmentSize %d", p, st.Size(), segmentSize)
		}
	}
}
