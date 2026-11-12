package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// perRowCatchUp is the write path catch-up had before rows were batched: one
// Append, and so one sync, per record. It is kept here as the reference the
// batched path is compared against. limit stops it after that many rows, the
// state a crash between two rows left behind; zero writes them all.
func perRowCatchUp(t *testing.T, cfg CatchUpConfig, limit int) {
	t.Helper()
	var watermark uint64
	if tail, ok := cfg.Translator.PeekTail(); ok {
		watermark = tail.FigaroLT
	}
	entries, _ := store.TailAfter(cfg.Log, watermark)
	d := NewDeriver(cfg.Form, cfg.Studies)
	d.SeedAt(cfg.Log, SeedLT(cfg.Translator))
	written := 0
	for _, entry := range entries {
		if limit > 0 && written == limit {
			return
		}
		msg, snap, translatable := d.Next(entry)
		if !translatable {
			continue
		}
		msg, _ = SendableImages(msg)
		encoded, err := cfg.Encode(msg, snap)
		if err != nil {
			t.Fatal(err)
		}
		empty := len(encoded) == 0
		if empty {
			encoded = []json.RawMessage{}
		}
		hash, err := store.FigaroHash(entry.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cfg.Translator.Append(store.Entry[[]json.RawMessage]{
			FigaroLT: entry.LT, Payload: encoded, Fingerprint: cfg.Fingerprint, FigaroHash: hash,
		}); err != nil {
			t.Fatal(err)
		}
		written++
		if !empty {
			d.Commit(entry, msg)
		}
	}
}

type batchFixture struct {
	root    string
	backend *store.XwalBackend
	aria    string
	log     store.Log[message.Message]
}

func openBatchFixture(t *testing.T, root string, segmentSize int) *batchFixture {
	t.Helper()
	b, err := store.NewXwalBackend(root, segmentSize)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return &batchFixture{root: root, backend: b}
}

// newAria gives the fixture a conversation with n synthetic records: text of
// varying size, some with several blocks.
func (f *batchFixture) newAria(t *testing.T, n int) {
	t.Helper()
	outfit, err := f.backend.CreateOutfit("batch", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"skills.x": json.RawMessage(`1`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if f.aria, err = f.backend.CreateConversation(outfit); err != nil {
		t.Fatal(err)
	}
	f.openLog(t)
	for i := 0; i < n; i++ {
		role := message.RoleInput
		if i%2 == 1 {
			role = message.RoleOutput
		}
		content := []message.Content{message.TextContent(fmt.Sprintf("record %d %s", i, strings.Repeat("x", (i*37)%400)))}
		if i%5 == 0 {
			content = append(content, message.TextContent("second block"))
		}
		if _, err := f.log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: role, Content: content, Timestamp: int64(1000 + i),
		}}); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *batchFixture) openLog(t *testing.T) {
	t.Helper()
	log, err := f.backend.OpenFigIR(f.aria)
	if err != nil {
		t.Fatal(err)
	}
	f.log = log
}

func (f *batchFixture) reopen(t *testing.T, segmentSize int) {
	t.Helper()
	if err := f.backend.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := store.NewXwalBackend(f.root, segmentSize)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	f.backend = b
	f.openLog(t)
}

// config encodes one wire message per block and nothing at all for every
// seventh record, so the rows include empty ones.
func (f *batchFixture) config(t *testing.T, channel string) CatchUpConfig {
	t.Helper()
	rows, err := f.backend.OpenTranslator(f.aria, channel)
	if err != nil {
		t.Fatal(err)
	}
	return CatchUpConfig{
		Log:         f.log,
		Translator:  rows,
		Fingerprint: "test/v1",
		Provider:    channel,
		Encode: func(msg message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			if msg.Timestamp%7 == 0 {
				return nil, nil
			}
			var out []json.RawMessage
			for _, c := range msg.Content {
				body, err := json.Marshal(map[string]any{"role": string(msg.Role), "text": c.Text, "ts": msg.Timestamp})
				if err != nil {
					return nil, err
				}
				out = append(out, body)
			}
			return out, nil
		},
	}
}

// rowDump is everything a row is, payload bytes included: LT, FigaroLT,
// fingerprint, record hash, payload.
func rowDump(rows store.Log[[]json.RawMessage]) []string {
	var out []string
	store.ScanAll(rows, func(e store.Entry[[]json.RawMessage]) bool {
		payload, _ := json.Marshal(e.Payload)
		out = append(out, fmt.Sprintf("%d %d %s %s %s", e.LT, e.FigaroLT, e.Fingerprint, e.FigaroHash, payload))
		return true
	})
	return out
}

func sameRows(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(want) == 0 {
		t.Fatalf("%s: the reference has no rows", what)
	}
	if len(got) != len(want) {
		t.Fatalf("%s: %d rows, the per-row path wrote %d", what, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: row %d differs\n got %s\nwant %s", what, i, got[i], want[i])
		}
	}
}

const (
	batchedChannel = "anthropic"
	perRowChannel  = "copilot-messages"
)

// The batched catch-up writes the rows the per-row path wrote: the same LT,
// FigaroLT, fingerprint, record hash and payload bytes for every row, whether
// the catch-up fits one batch or is cut into many, and the same rows are
// there after the store reopens.
func TestBatchedCatchUpWritesTheRowsThePerRowPathWrote(t *testing.T) {
	for _, batchBytes := range []int{0, 1, 700} {
		t.Run(fmt.Sprintf("batch of %d bytes", batchBytes), func(t *testing.T) {
			f := openBatchFixture(t, t.TempDir(), 4096)
			f.newAria(t, 240)
			perRowCatchUp(t, f.config(t, perRowChannel), 0)
			want := rowDump(f.config(t, perRowChannel).Translator)

			cfg := f.config(t, batchedChannel)
			cfg.BatchBytes = batchBytes
			stats, err := CatchUp(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if stats.Encoded+stats.Unwritten != len(want) || stats.Unwritten == 0 {
				t.Fatalf("stats %+v for %d rows; the log must hold empty rows too", stats, len(want))
			}
			for i := range want {
				want[i] = strings.Replace(want[i], perRowChannel, batchedChannel, 1)
			}
			sameRows(t, "live", rowDump(cfg.Translator), want)

			f.reopen(t, 4096)
			sameRows(t, "after reopen", rowDump(f.config(t, batchedChannel).Translator), want)
			if again, err := CatchUp(f.config(t, batchedChannel)); err != nil || again.Visited != 0 {
				t.Fatalf("a second catch-up visited %d records (err %v), want none", again.Visited, err)
			}
		})
	}
}

// A durable prefix of a catch-up's rows, whatever left it (a crash between
// two per-row writes, a batch that synced before a later one failed, a
// buffer that synced inline), is resumed by the next catch-up: the tail-hash
// rule accepts the tail, the hole check finds no hole, and the rows end up
// the ones an uninterrupted run writes.
func TestCatchUpResumesFromADurablePrefix(t *testing.T) {
	for _, prefix := range []int{1, 6, 7, 100, 239} {
		t.Run(fmt.Sprintf("%d rows", prefix), func(t *testing.T) {
			f := openBatchFixture(t, t.TempDir(), 4096)
			f.newAria(t, 240)
			perRowCatchUp(t, f.config(t, perRowChannel), 0)
			want := rowDump(f.config(t, perRowChannel).Translator)

			perRowCatchUp(t, f.config(t, batchedChannel), prefix)
			f.reopen(t, 4096)
			cfg := f.config(t, batchedChannel)
			if got := cfg.Translator.Len(); got != prefix {
				t.Fatalf("the prefix holds %d rows, want %d", got, prefix)
			}
			stats, err := CatchUp(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if stats.RepairedAt != 0 {
				t.Fatalf("the hole check cut a clean prefix at %d", stats.RepairedAt)
			}
			if stats.Visited != len(want)-prefix {
				t.Fatalf("visited %d records after a prefix of %d, want %d", stats.Visited, prefix, len(want)-prefix)
			}
			sameRows(t, "resumed", rowDump(cfg.Translator), want)
		})
	}
}

// refusingRows is a row log whose batch write can be made to fail whole, as
// the store's does when its sync fails: none of the batch's rows remain.
type refusingRows struct {
	*store.MemLog[[]json.RawMessage]
	refuse  func(batch int) bool
	batches int
}

func (r *refusingRows) AppendBatch(rows []store.Entry[[]json.RawMessage]) ([]store.Entry[[]json.RawMessage], error) {
	r.batches++
	if r.refuse != nil && r.refuse(r.batches) {
		return nil, errors.New("sync: input/output error")
	}
	out := make([]store.Entry[[]json.RawMessage], 0, len(rows))
	for _, e := range rows {
		stamped, err := r.MemLog.Append(e)
		if err != nil {
			return nil, err
		}
		out = append(out, stamped)
	}
	return out, nil
}

// A catch-up whose final sync fails fails the send and leaves no row of the
// failed batch, so no request can be built from rows that are not on disk;
// the next catch-up writes them.
func TestACatchUpWhoseSyncFailsFailsTheSendAndLeavesNoRow(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	for i := 0; i < 30; i++ {
		appendProjectionMessage(t, log, fmt.Sprintf("body %d", i))
	}
	reference := store.NewMemLog[[]json.RawMessage]()
	if _, err := CatchUp(catchUpTestConfig(log, reference)); err != nil {
		t.Fatal(err)
	}

	rows := &refusingRows{MemLog: store.NewMemLog[[]json.RawMessage](), refuse: func(int) bool { return true }}
	cfg := catchUpTestConfig(log, rows)
	var reportedAt uint64
	cfg.ReportWriteError = func(lt uint64, _ error) { reportedAt = lt }
	stats, err := CatchUp(cfg)
	if err == nil {
		t.Fatal("a catch-up whose sync failed was reported as done")
	}
	if rows.batches != 1 {
		t.Fatalf("%d batch writes for a catch-up that fits one batch, want one sync", rows.batches)
	}
	if rows.Len() != 0 || stats.Encoded != 0 {
		t.Fatalf("after a failed sync %d rows are readable and %d counted as written", rows.Len(), stats.Encoded)
	}
	if reportedAt != 1 {
		t.Fatalf("the write error was reported at record %d, want the batch's first, 1", reportedAt)
	}

	rows.refuse = nil
	if stats, err = CatchUp(cfg); err != nil || stats.Encoded != 30 {
		t.Fatalf("the next catch-up wrote %d rows (err %v), want 30", stats.Encoded, err)
	}
	sameRows(t, "after the retry", rowDump(rows), rowDump(reference))
}

// When a long catch-up is cut into batches and a later batch fails, the
// batches before it are a durable prefix and stay; the failed one leaves
// nothing; the send fails; the next catch-up resumes after the prefix.
func TestAFailedLaterBatchKeepsTheDurablePrefix(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	for i := 0; i < 30; i++ {
		appendProjectionMessage(t, log, fmt.Sprintf("body %d", i))
	}
	reference := store.NewMemLog[[]json.RawMessage]()
	if _, err := CatchUp(catchUpTestConfig(log, reference)); err != nil {
		t.Fatal(err)
	}

	rows := &refusingRows{MemLog: store.NewMemLog[[]json.RawMessage](), refuse: func(batch int) bool { return batch == 3 }}
	cfg := catchUpTestConfig(log, rows)
	one, _ := cfg.Encode(message.Message{Role: message.RoleInput, LogicalTime: 1}, form.Snapshot{})
	cfg.BatchBytes = 5 * len(one[0])
	stats, err := CatchUp(cfg)
	if err == nil {
		t.Fatal("a catch-up whose third batch failed was reported as done")
	}
	kept := rows.Len()
	if kept == 0 || kept >= 30 || stats.Encoded != kept {
		t.Fatalf("%d rows kept and %d counted after the third batch failed, want the first two batches", kept, stats.Encoded)
	}
	sameRows(t, "the prefix", rowDump(rows), rowDump(reference)[:kept])

	rows.refuse = nil
	if stats, err = CatchUp(cfg); err != nil || stats.Visited != 30-kept {
		t.Fatalf("the next catch-up visited %d records (err %v), want %d", stats.Visited, err, 30-kept)
	}
	sameRows(t, "after the retry", rowDump(rows), rowDump(reference))
}

// An encode failure part way through still writes the rows before it, as the
// per-row path did, and reports the encode error.
func TestRowsBeforeAnEncodeFailureAreWritten(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	for i := 0; i < 10; i++ {
		appendProjectionMessage(t, log, fmt.Sprintf("body %d", i))
	}
	rows := store.NewMemLog[[]json.RawMessage]()
	cfg := catchUpTestConfig(log, rows)
	encode := cfg.Encode
	cfg.Encode = func(msg message.Message, snap form.Snapshot) ([]json.RawMessage, error) {
		if msg.LogicalTime == 8 {
			return nil, errors.New("cannot encode")
		}
		return encode(msg, snap)
	}
	stats, err := CatchUp(cfg)
	if err == nil || !strings.Contains(err.Error(), "encode at 8") {
		t.Fatalf("err = %v, want the encode failure at 8", err)
	}
	if rows.Len() != 7 || stats.Encoded != 7 {
		t.Fatalf("%d rows written and %d counted before the failure, want 7", rows.Len(), stats.Encoded)
	}
}

// The same promise against a real disk failure. The row channel's directory
// is made read-only, so the batch fails when it needs a new segment file:
// part of it has reached a sealed segment and the cut that would take it
// back cannot run either. Whatever is on disk after that is a prefix of the
// rows, and a process that reopens the store resumes from it.
func TestACatchUpThatFailedOnDiskResumesAfterReopen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test relies on")
	}
	f := openBatchFixture(t, t.TempDir(), 2048)
	f.newAria(t, 2)
	if _, err := CatchUp(f.config(t, batchedChannel)); err != nil {
		t.Fatal(err)
	}
	before := f.config(t, batchedChannel).Translator.Len()
	for i := 2; i < 120; i++ {
		if _, err := f.log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role:      message.RoleInput,
			Content:   []message.Content{message.TextContent(fmt.Sprintf("late record %d %s", i, strings.Repeat("y", 150)))},
			Timestamp: int64(1000 + i),
		}}); err != nil {
			t.Fatal(err)
		}
	}
	perRowCatchUp(t, f.config(t, perRowChannel), 0)
	want := rowDump(f.config(t, perRowChannel).Translator)
	for i := range want {
		want[i] = strings.Replace(want[i], perRowChannel, batchedChannel, 1)
	}

	var dir string
	_ = filepath.WalkDir(f.root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && strings.HasSuffix(filepath.Dir(p), filepath.Join("translations-v2", batchedChannel)) {
			dir = p
		}
		return nil
	})
	if dir == "" {
		t.Fatal("no directory for the row channel")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	cfg := f.config(t, batchedChannel)
	_, failure := CatchUp(cfg)
	if failure == nil {
		t.Skip("the batch fit the open segment; this filesystem gave no failure to test")
	}
	if live := cfg.Translator.Len(); live >= len(want) {
		t.Fatalf("%d rows readable after a failed catch-up of %d", live, len(want))
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	f.reopen(t, 2048)
	cfg = f.config(t, batchedChannel)
	onDisk := rowDump(cfg.Translator)
	if len(onDisk) <= before || len(onDisk) >= len(want) {
		t.Fatalf("%d rows on disk after a catch-up from %d to %d failed (%v): the fixture must leave part of the batch behind",
			len(onDisk), before, len(want), failure)
	}
	sameRows(t, "the prefix on disk", onDisk, want[:len(onDisk)])
	stats, err := CatchUp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stats.RepairedAt != 0 || stats.Visited != len(want)-len(onDisk) {
		t.Fatalf("resume visited %d records and repaired at %d, want %d and no repair", stats.Visited, stats.RepairedAt, len(want)-len(onDisk))
	}
	sameRows(t, "resumed", rowDump(cfg.Translator), want)
}
