package xwal

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"
)

const batchChannel = "rows"

func openBatchStore(t *testing.T, dir string) *Store {
	t.Helper()
	opts := testStoreOptions()
	opts.NoBackgroundFlush = true
	opts.IdleUnload = -1
	opts.Opaque = []string{batchChannel}
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// seedBatchTrunk writes n main records and one row for the first of them, so
// a batch has a tail to land after and to be cut back to.
func seedBatchTrunk(t *testing.T, s *Store, n int) TrunkID {
	t.Helper()
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		lt, err := s.Append(tr, "ir", 0, []byte(fmt.Sprintf(`{"i":%d}`, i)), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SyncChannelThrough(tr, "ir", lt); err != nil {
			t.Fatal(err)
		}
	}
	lt, err := s.Append(tr, batchChannel, 2, []byte(`"seed"`), []byte(`"m"`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SyncChannelThrough(tr, batchChannel, lt); err != nil {
		t.Fatal(err)
	}
	return tr
}

func batchRows(from, to uint64) []ChannelRow {
	var rows []ChannelRow
	for lt := from; lt <= to; lt++ {
		rows = append(rows, ChannelRow{
			MainLT:  lt,
			Payload: []byte(fmt.Sprintf(`"row %d"`, lt)),
			Meta:    []byte(fmt.Sprintf(`"meta %d"`, lt)),
		})
	}
	return rows
}

type batchRecord struct {
	lt, mainLT    uint64
	payload, meta string
}

func readBatchChannel(t *testing.T, s *Store, tr TrunkID) []batchRecord {
	t.Helper()
	x, err := s.Head(tr)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	first, last, ok := x.ChannelBounds(batchChannel)
	if !ok || last == 0 {
		return nil
	}
	if first == 0 {
		first = 1
	}
	var out []batchRecord
	for lt := first; lt <= last; lt++ {
		r, err := x.ReadAt(batchChannel, lt)
		if err != nil {
			t.Fatalf("row %d of %d..%d does not read: %v", lt, first, last, err)
		}
		out = append(out, batchRecord{r.ChannelLT, r.MainLT, string(r.Payload), string(r.Meta)})
	}
	return out
}

// A batch is the rows a writer would have appended one at a time: the same
// channel LTs, main LTs, payloads and meta, in the same order, and on disk
// when the call returns.
func TestABatchWritesTheRowsOneAppendAtATimeWould(t *testing.T) {
	serialDir, batchDir := t.TempDir(), t.TempDir()
	serial, batch := openBatchStore(t, serialDir), openBatchStore(t, batchDir)
	serialTrunk, batchTrunk := seedBatchTrunk(t, serial, 12), seedBatchTrunk(t, batch, 12)
	rows := batchRows(3, 12)

	for _, r := range rows {
		lt, err := serial.Append(serialTrunk, batchChannel, r.MainLT, r.Payload, r.Meta)
		if err != nil {
			t.Fatal(err)
		}
		if err := serial.SyncChannelThrough(serialTrunk, batchChannel, lt); err != nil {
			t.Fatal(err)
		}
	}
	syncs := 0
	batch.Trunks.testBatchSync = func(sync func() error) error { syncs++; return sync() }
	first, err := batch.AppendBatch(batchTrunk, batchChannel, rows)
	if err != nil {
		t.Fatal(err)
	}
	batch.Trunks.testBatchSync = nil
	if first != 2 || syncs != 1 {
		t.Fatalf("batch landed at %d with %d syncs, want LT 2 and one sync", first, syncs)
	}

	want := readBatchChannel(t, serial, serialTrunk)
	if got := readBatchChannel(t, batch, batchTrunk); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("batched rows differ from serial rows:\n got %v\nwant %v", got, want)
	}
	if err := batch.Close(); err != nil {
		t.Fatal(err)
	}
	if err := serial.Close(); err != nil {
		t.Fatal(err)
	}
	again := openBatchStore(t, batchDir)
	defer again.Close()
	if got := readBatchChannel(t, again, batchTrunk); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("after reopen the batched rows differ from serial rows:\n got %v\nwant %v", got, want)
	}
}

// A batch whose sync fails leaves no row: not in the live store, where the
// next catch-up would build a request from it, and not after a reopen. Both
// shapes of failure are covered: the rows never reached the file, and the
// rows reached the file and the sync then reported an error.
func TestABatchWhoseSyncFailsLeavesNoRow(t *testing.T) {
	cases := map[string]func(sync func() error) error{
		"rows only buffered": func(func() error) error { return errors.New("input/output error") },
		"rows in the file": func(sync func() error) error {
			if err := sync(); err != nil {
				return err
			}
			return errors.New("input/output error")
		},
	}
	for name, fail := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s := openBatchStore(t, dir)
			tr := seedBatchTrunk(t, s, 12)
			before := readBatchChannel(t, s, tr)

			s.Trunks.testBatchSync = fail
			if _, err := s.AppendBatch(tr, batchChannel, batchRows(3, 12)); err == nil {
				t.Fatal("a batch whose sync failed was reported as written")
			}
			s.Trunks.testBatchSync = nil
			if got := readBatchChannel(t, s, tr); fmt.Sprint(got) != fmt.Sprint(before) {
				t.Fatalf("rows of a failed batch are readable:\n got %v\nwant %v", got, before)
			}

			// The channel is whole again: the same batch lands where the
			// refused one would have.
			first, err := s.AppendBatch(tr, batchChannel, batchRows(3, 12))
			if err != nil {
				t.Fatal(err)
			}
			if first != 2 {
				t.Fatalf("the retried batch landed at %d, want 2", first)
			}
			after := readBatchChannel(t, s, tr)
			if len(after) != 11 {
				t.Fatalf("%d rows after the retry, want 11", len(after))
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			again := openBatchStore(t, dir)
			defer again.Close()
			if got := readBatchChannel(t, again, tr); fmt.Sprint(got) != fmt.Sprint(after) {
				t.Fatalf("rows differ after reopen:\n got %v\nwant %v", got, after)
			}
		})
	}
}

// The refused rows are gone from disk too, not only from the buffer: a
// process that reopens the store without retrying finds the channel as it
// stood before the batch.
func TestAFailedBatchIsGoneAfterReopen(t *testing.T) {
	dir := t.TempDir()
	s := openBatchStore(t, dir)
	tr := seedBatchTrunk(t, s, 12)
	before := readBatchChannel(t, s, tr)
	s.Trunks.testBatchSync = func(sync func() error) error {
		if err := sync(); err != nil {
			return err
		}
		return errors.New("input/output error")
	}
	if _, err := s.AppendBatch(tr, batchChannel, batchRows(3, 12)); err == nil {
		t.Fatal("a batch whose sync failed was reported as written")
	}
	s.Trunks.testBatchSync = nil
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again := openBatchStore(t, dir)
	defer again.Close()
	if got := readBatchChannel(t, again, tr); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Fatalf("rows of a failed batch came back after reopen:\n got %v\nwant %v", got, before)
	}
}

// Nothing outside the batch sees a row before it is durable. A reader that
// asks for the head while the rows are buffered waits for the batch to
// resolve; when it fails the reader finds no batch row.
func TestAReaderWaitsOutABufferedBatch(t *testing.T) {
	dir := t.TempDir()
	s := openBatchStore(t, dir)
	defer s.Close()
	tr := seedBatchTrunk(t, s, 12)
	before := readBatchChannel(t, s, tr)

	readDone := make(chan []batchRecord, 1)
	s.Trunks.testBatchSync = func(func() error) error {
		go func() { readDone <- readBatchChannel(t, s, tr) }()
		select {
		case rows := <-readDone:
			t.Errorf("a reader opened the head while a batch was buffered and saw %d rows", len(rows))
			readDone <- rows
		case <-time.After(100 * time.Millisecond):
		}
		return errors.New("input/output error")
	}
	if _, err := s.AppendBatch(tr, batchChannel, batchRows(3, 12)); err == nil {
		t.Fatal("a batch whose sync failed was reported as written")
	}
	s.Trunks.testBatchSync = nil
	if got := <-readDone; fmt.Sprint(got) != fmt.Sprint(before) {
		t.Fatalf("a concurrent reader saw rows of a failed batch:\n got %v\nwant %v", got, before)
	}
}

// A fork that arrives while a batch is buffered waits too, so the base it
// takes in the channel never names a row that was later refused.
func TestAForkWaitsOutABufferedBatch(t *testing.T) {
	dir := t.TempDir()
	s := openBatchStore(t, dir)
	defer s.Close()
	tr := seedBatchTrunk(t, s, 12)
	before := readBatchChannel(t, s, tr)

	type forked struct {
		trunk string
		err   error
	}
	forkDone := make(chan forked, 1)
	s.Trunks.testBatchSync = func(func() error) error {
		go func() {
			alt, err := s.Fork(tr, 0)
			forkDone <- forked{alt, err}
		}()
		select {
		case f := <-forkDone:
			t.Errorf("a fork completed while a batch was buffered (trunk %q, err %v)", f.trunk, f.err)
			forkDone <- f
		case <-time.After(100 * time.Millisecond):
		}
		return errors.New("input/output error")
	}
	if _, err := s.AppendBatch(tr, batchChannel, batchRows(3, 12)); err == nil {
		t.Fatal("a batch whose sync failed was reported as written")
	}
	s.Trunks.testBatchSync = nil

	f := <-forkDone
	if f.err != nil {
		t.Fatal(f.err)
	}
	if got := readBatchChannel(t, s, TrunkID(f.trunk)); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Fatalf("a fork taken during a failed batch inherited its rows:\n got %v\nwant %v", got, before)
	}
	child, err := s.Head(f.trunk)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := child.ChannelForkBase(batchChannel)
	child.Close()
	if base != 2 {
		t.Fatalf("the fork's base in the channel is %d, want 2: one past the row that was durable", base)
	}
}

// A batch that cannot start (the channel is unknown to the raw Trunks) writes
// nothing and says so; the store creates the channel and retries.
func TestABatchCreatesItsChannel(t *testing.T) {
	dir := t.TempDir()
	opts := testStoreOptions()
	opts.NoBackgroundFlush = true
	opts.IdleUnload = -1
	s, err := OpenStore(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Append(tr, "ir", 0, []byte(`{}`), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Trunks.AppendChannelBatch(tr, "late", batchRows(2, 3)); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("a batch into an unknown channel: %v, want ErrNoChannel", err)
	}
	first, err := s.AppendBatch(tr, "late", batchRows(2, 3))
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 {
		t.Fatalf("the first batch of a new channel landed at %d, want 1", first)
	}
	x, err := s.Head(tr)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	r, err := x.ReadAt("late", 2)
	if err != nil {
		t.Fatal(err)
	}
	if r.MainLT != 3 || !bytes.Equal(r.Payload, []byte(`"row 3"`)) {
		t.Fatalf("row 2 of the new channel is %d %s", r.MainLT, r.Payload)
	}
}
