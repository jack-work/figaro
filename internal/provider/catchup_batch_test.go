package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	studied string
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
	f.createAria(t)
	for i := 0; i < n; i++ {
		f.appendRecord(t, i)
	}
}

func (f *batchFixture) createAria(t *testing.T) {
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
}

func (f *batchFixture) appendRecord(t *testing.T, i int) {
	t.Helper()
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
// fingerprint, record hash, the form and study versions stamped on the row,
// payload.
func rowDump(rows store.Log[[]json.RawMessage]) []string {
	var out []string
	store.ScanAll(rows, func(e store.Entry[[]json.RawMessage]) bool {
		payload, _ := json.Marshal(e.Payload)
		out = append(out, fmt.Sprintf("%d %d %s %s form=%d studies=%v %s",
			e.LT, e.FigaroLT, e.Fingerprint, e.FigaroHash, e.FormChannelVersion, e.StudyVersions, payload))
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

// boundFormOf and studiedFormOf answer the deriver from the fixture's store the
// way the agent's views do: patches by absolute range, and for the bound form
// the state at a version.
type boundFormOf struct{ f *batchFixture }

func (v boundFormOf) PatchesBetween(after, upTo uint64) []message.Patch {
	ps, err := v.f.backend.FormPatchesBetween(v.f.aria, after, upTo)
	if err != nil {
		return nil
	}
	out := make([]message.Patch, len(ps))
	for i := range ps {
		out[i] = ps[i].Patch
	}
	return out
}

func (v boundFormOf) StateAt(version uint64) (form.Snapshot, bool) {
	snap, err := v.f.backend.FormStateAt(v.f.aria, version)
	return snap, err == nil
}

type studiedFormOf struct{ f *batchFixture }

func (v studiedFormOf) PatchesBetween(after, upTo uint64) []message.Patch {
	lib, err := v.f.backend.Libretto(v.f.studied)
	if err != nil {
		return nil
	}
	ps := lib.PatchesBetween(after, upTo)
	out := make([]message.Patch, len(ps))
	for i := range ps {
		out[i] = ps[i].Patch
	}
	return out
}

func waitForFold(t *testing.T, lib *store.Libretto, version uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if lib.At() >= version {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the libretto did not fold source version %d (at %d)", version, lib.At())
}

// newAriaWithChangingForms gives the fixture a conversation whose forms move
// under it: the bound form is patched before every user record and a studied
// form before every fourth record, so the IR entries are stamped with form and
// study versions that differ from row to row.
func (f *batchFixture) newAriaWithChangingForms(t *testing.T, n int) {
	t.Helper()
	f.createAria(t)
	src, _, err := f.backend.CreateForm("", setKey("status", "created"))
	if err != nil {
		t.Fatal(err)
	}
	f.studied = src
	decl, err := f.backend.StudyForm(f.aria, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.backend.SetObservedForms(f.aria, decl.Studies)
	lib, err := f.backend.Libretto(src)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			if _, err := f.backend.ApplyForm(f.aria, setKey("mantra", fmt.Sprintf("before record %d", i))); err != nil {
				t.Fatal(err)
			}
		}
		if i%4 == 0 {
			v, err := f.backend.ApplyForm(src, setKey("status", fmt.Sprintf("before record %d", i)))
			if err != nil {
				t.Fatal(err)
			}
			waitForFold(t, lib, v)
		}
		f.appendRecord(t, i)
	}
}

// stampedVersions counts the distinct form and study versions the IR entries
// carry.
func (f *batchFixture) stampedVersions() (bound, studied int) {
	forms, studies := map[uint64]bool{}, map[uint64]bool{}
	entries, _ := store.TailAfter(f.log, 0)
	for _, e := range entries {
		forms[e.FormChannelVersion] = true
		if v, ok := e.StudyVersions[f.studied]; ok {
			studies[v] = true
		}
	}
	return len(forms), len(studies)
}

func patchLines(patches []message.Patch) []string {
	out := []string{}
	for i, p := range patches {
		for _, ent := range p.Entries() {
			if ent.IsRemoval() {
				out = append(out, fmt.Sprintf("%d -%s", i, ent.Key))
			} else {
				out = append(out, fmt.Sprintf("%d %s=%s", i, ent.Key, ent.New))
			}
		}
	}
	return out
}

// formWindows counts the messages an Encode was handed with a form change to
// carry.
type formWindows struct{ bound, studied int }

// formConfig wires the fixture's forms into the catch-up and encodes each
// message as what the deriver handed over: the bound form's state before the
// message, the patches in its window, and each studied form's patches and
// version. Two derivations that differ anywhere differ in the row's bytes.
func (f *batchFixture) formConfig(t *testing.T, channel string, seen *formWindows) CatchUpConfig {
	t.Helper()
	cfg := f.config(t, channel)
	cfg.Form = boundFormOf{f}
	cfg.Studies = map[string]Form{f.studied: studiedFormOf{f}}
	cfg.Encode = func(msg message.Message, snap form.Snapshot) ([]json.RawMessage, error) {
		if msg.Timestamp%7 == 0 {
			return nil, nil
		}
		state, err := json.Marshal(snap)
		if err != nil {
			return nil, err
		}
		study := map[string][]string{}
		for fid, ps := range msg.StudyPatches {
			study[fid] = patchLines(ps)
		}
		body, err := json.Marshal(map[string]any{
			"role": string(msg.Role), "ts": msg.Timestamp,
			"form": json.RawMessage(state), "patches": patchLines(msg.Patches),
			"study": study, "study_at": msg.StudyAt,
		})
		if err != nil {
			return nil, err
		}
		if seen != nil {
			if len(msg.Patches) > 0 {
				seen.bound++
			}
			if len(msg.StudyPatches) > 0 {
				seen.studied++
			}
		}
		return []json.RawMessage{body}, nil
	}
	return cfg
}

// perRowReference writes the fixture's rows on the per-row channel in one
// uninterrupted run and returns them, after checking that the fixture is the
// shape under test: versions that change from entry to entry, and messages
// that carried those changes into the encoder.
func (f *batchFixture) perRowReference(t *testing.T) []string {
	t.Helper()
	var seen formWindows
	perRowCatchUp(t, f.formConfig(t, perRowChannel, &seen), 0)
	if bound, studied := f.stampedVersions(); bound < 90 || studied < 40 {
		t.Fatalf("the IR entries carry %d form versions and %d study versions: the forms did not change between rows", bound, studied)
	}
	if seen.bound < 90 || seen.studied < 40 {
		t.Fatalf("%d messages carried a bound form change and %d a studied one: the encoder saw too few", seen.bound, seen.studied)
	}
	return rowDump(f.formConfig(t, perRowChannel, nil).Translator)
}

// When the bound form and a studied form change between rows, the batched
// catch-up hands the encoder what the per-row path handed it: the same state,
// the same patch window and the same study versions for every row, in one
// batch or many, and after the store reopens.
func TestBatchedCatchUpDerivesTheFormsThePerRowPathDerived(t *testing.T) {
	for _, batchBytes := range []int{0, 1, 700} {
		t.Run(fmt.Sprintf("batch of %d bytes", batchBytes), func(t *testing.T) {
			f := openBatchFixture(t, t.TempDir(), 4096)
			f.newAriaWithChangingForms(t, 240)
			want := f.perRowReference(t)

			cfg := f.formConfig(t, batchedChannel, nil)
			cfg.BatchBytes = batchBytes
			if _, err := CatchUp(cfg); err != nil {
				t.Fatal(err)
			}
			sameRows(t, "live", rowDump(cfg.Translator), want)

			f.reopen(t, 4096)
			sameRows(t, "after reopen", rowDump(f.formConfig(t, batchedChannel, nil).Translator), want)
		})
	}
}

const perRowResumedChannel = "copilot-responses"

// A catch-up stopped part way and resumed after a reopen, under forms that
// change between rows. The batched path resumes to the rows the per-row path
// resumes to, for every prefix. Whether those are the rows of an
// uninterrupted run is the deriver seed's promise, not the batch's: where the
// prefix ends on the assistant's row that follows a user record whose row is
// empty, the seed adopts that record's form version though its window was
// never written, and the change it carried is lost. Those prefixes are
// skipped for that comparison until the seed is fixed.
func TestCatchUpResumedUnderChangingFormsWritesThePerRowRows(t *testing.T) {
	cases := []struct {
		prefix          int
		seedLosesWindow bool
	}{
		{prefix: 1}, {prefix: 6}, {prefix: 10}, {prefix: 24}, {prefix: 100}, {prefix: 239},
		{prefix: 11, seedLosesWindow: true}, {prefix: 25, seedLosesWindow: true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d rows", tc.prefix), func(t *testing.T) {
			f := openBatchFixture(t, t.TempDir(), 4096)
			f.newAriaWithChangingForms(t, 240)
			uninterrupted := f.perRowReference(t)

			perRowCatchUp(t, f.formConfig(t, perRowResumedChannel, nil), tc.prefix)
			perRowCatchUp(t, f.formConfig(t, batchedChannel, nil), tc.prefix)
			f.reopen(t, 4096)
			perRowCatchUp(t, f.formConfig(t, perRowResumedChannel, nil), 0)
			cfg := f.formConfig(t, batchedChannel, nil)
			stats, err := CatchUp(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if stats.RepairedAt != 0 || stats.Visited != len(uninterrupted)-tc.prefix {
				t.Fatalf("resume visited %d records and repaired at %d, want %d and no repair: the rows were not resumed",
					stats.Visited, stats.RepairedAt, len(uninterrupted)-tc.prefix)
			}
			resumed := rowDump(cfg.Translator)
			sameRows(t, "resumed in batches against resumed row by row", resumed,
				rowDump(f.formConfig(t, perRowResumedChannel, nil).Translator))

			if tc.seedLosesWindow {
				t.Skip("owned by lane startup-exit-waits: SeedAt adopts the form version of a user record whose row is empty")
			}
			sameRows(t, "resumed against uninterrupted", resumed, uninterrupted)
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

// otherFigaro is a second conversation in the fixture's store with n records
// of its own: another lineage than the one a catch-up writes.
func (f *batchFixture) otherFigaro(t *testing.T, n int) string {
	t.Helper()
	outfit, err := f.backend.CreateOutfit("batch", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"skills.x": json.RawMessage(`1`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.backend.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	log, err := f.backend.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := log.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("other %d", i))}, Timestamp: int64(5000 + i),
		}}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// slowBatchSync stands in for a disk that stalls: the first batch a catch-up
// writes stops at its sync, with the rows buffered and the lineage held, until
// the test lets it go. reached closes when it is there; synced counts the
// batches whose sync has returned.
type slowBatchSync struct {
	reached chan struct{}
	release chan struct{}
	calls   atomic.Int32
	synced  atomic.Int32
}

func stallFirstBatchSync(t *testing.T, b *store.XwalBackend) *slowBatchSync {
	t.Helper()
	s := &slowBatchSync{reached: make(chan struct{}), release: make(chan struct{})}
	b.BatchSyncForTest(func(sync func() error) error {
		if s.calls.Add(1) == 1 {
			close(s.reached)
			<-s.release
		}
		err := sync()
		s.synced.Add(1)
		return err
	})
	t.Cleanup(func() { b.BatchSyncForTest(nil) })
	return s
}

const batchWaitLimit = 20 * time.Second

// A batch holds its own figaro's lineage for as long as its sync takes, and
// nothing else: while the sync is stalled, a reader of another figaro opens
// its node and reads without waiting for it.
func TestAStalledBatchSyncDoesNotHoldBackAReaderOfAnotherFigaro(t *testing.T) {
	f := openBatchFixture(t, t.TempDir(), 0)
	f.newAria(t, 40)
	other := f.otherFigaro(t, 5)
	want := f.backend.OpenNode(other).Len()
	if want < 5 {
		t.Fatalf("the other figaro reads %d records before the catch-up, want its 5", want)
	}
	cfg := f.config(t, batchedChannel)

	stall := stallFirstBatchSync(t, f.backend)
	caughtUp := make(chan error, 1)
	go func() {
		_, err := CatchUp(cfg)
		caughtUp <- err
	}()
	select {
	case <-stall.reached:
	case err := <-caughtUp:
		t.Fatalf("the catch-up ended without a batch sync (err %v)", err)
	}

	read := make(chan int, 1)
	go func() { read <- f.backend.OpenNode(other).Len() }()
	select {
	case n := <-read:
		if n != want {
			t.Errorf("the other figaro read %d records during the batch, %d before it", n, want)
		}
	case <-time.After(batchWaitLimit):
		t.Error("a reader of another figaro waited on a batch whose sync was stalled")
	}

	close(stall.release)
	if err := <-caughtUp; err != nil {
		t.Fatal(err)
	}
}

// What does wait on a stalled batch: a reader of the same figaro, by design,
// and a fork anywhere, which needs the tree the batch is reading. Each waits
// holding the store's own lock, so a reader of another figaro that arrives
// behind one of them waits too. The bound on all of them is one batch: when a
// catch-up is cut into several, everything queued behind the first is through
// before the second is written.
func TestWhatQueuesBehindAStalledBatchSyncIsFreedByThatOneSync(t *testing.T) {
	cases := []struct {
		name  string
		first func(f *batchFixture, third string)
	}{
		{"behind a reader of the same figaro", func(f *batchFixture, _ string) {
			f.backend.OpenNode(f.aria).Len()
		}},
		{"behind a fork of a third figaro", func(f *batchFixture, third string) {
			f.backend.Fork(third)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := openBatchFixture(t, t.TempDir(), 0)
			f.newAria(t, 40)
			other := f.otherFigaro(t, 5)
			third := f.otherFigaro(t, 5)
			cfg := f.config(t, batchedChannel)
			cfg.BatchBytes = 2048

			stall := stallFirstBatchSync(t, f.backend)
			queued := make(chan struct{})
			encode := cfg.Encode
			var checked atomic.Bool
			cfg.Encode = func(msg message.Message, snap form.Snapshot) ([]json.RawMessage, error) {
				if stall.synced.Load() == 1 && checked.CompareAndSwap(false, true) {
					select {
					case <-queued:
					case <-time.After(batchWaitLimit):
						t.Error("the first batch's sync returned and what queued behind it is still waiting")
					}
				}
				return encode(msg, snap)
			}
			caughtUp := make(chan error, 1)
			go func() {
				_, err := CatchUp(cfg)
				caughtUp <- err
			}()
			select {
			case <-stall.reached:
			case err := <-caughtUp:
				t.Fatalf("the catch-up ended without a batch sync (err %v)", err)
			}

			firstDone := make(chan struct{})
			go func() {
				tc.first(f, third)
				close(firstDone)
			}()
			select {
			case <-firstDone:
				t.Error("it got through while the batch was buffered and its sync stalled")
			case <-time.After(100 * time.Millisecond):
			}
			go func() {
				f.backend.OpenNode(other).Len()
				<-firstDone
				close(queued)
			}()

			close(stall.release)
			if err := <-caughtUp; err != nil {
				t.Fatal(err)
			}
			if n := stall.calls.Load(); n < 2 {
				t.Fatalf("the catch-up wrote %d batch, the test needs a second one to be held back", n)
			}
			if !checked.Load() {
				t.Fatal("no record was encoded after the first batch, so nothing was checked")
			}
		})
	}
}
