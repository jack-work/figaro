package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/formgen"
)

// The fold before it ran in one pass: decode the watermark, apply one record,
// encode, once per record. Kept here as the reference the one-pass fold must
// reproduce byte for byte.
func foldOneRecordAtATime(state []byte, records [][]byte) ([]byte, error) {
	for _, raw := range records {
		snap, seqs, err := decodeFormWatermark(state)
		if err != nil {
			return nil, err
		}
		var rec formRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, err
		}
		if k := rec.origin().key(); rec.Seq > 0 && rec.Seq > seqs[k] {
			if seqs == nil {
				seqs = map[string]uint64{}
			}
			seqs[k] = rec.Seq
		}
		if state, err = encodeFormWatermark(snap.Apply(rec.Patch), seqs); err != nil {
			return nil, err
		}
	}
	return state, nil
}

// A watermark written by the one-pass fold must be the bytes the old
// per-record fold wrote, so stores keep opening both ways and a header
// computed by either code agrees with the other's replay.
func TestFormFoldInOnePassWritesThePerRecordBytes(t *testing.T) {
	clients := []string{"", "", "r", "q"}
	principals := []string{"", "user:gluck@local", "sandbox:chat@local:box"}
	for seed := uint64(1); seed <= 3000; seed++ {
		g := formgen.New(seed)
		base := g.Snapshot()
		var seqs map[string]uint64
		if seed%3 == 0 {
			seqs = map[string]uint64{(Origin{Client: "r"}).key(): seed % 5}
		}
		header, err := encodeFormWatermark(base, seqs)
		if err != nil {
			t.Fatal(err)
		}
		state := base
		var records [][]byte
		next := map[string]uint64{}
		for n := 1 + g.R.IntN(12); n > 0; n-- {
			p := g.Patch(state)
			rec := formRecord{Patch: p, Client: clients[g.R.IntN(len(clients))]}
			if rec.Client != "" {
				rec.Principal = principals[g.R.IntN(len(principals))]
				k := rec.origin().key()
				if next[k] == 0 {
					next[k] = 1
				}
				rec.Seq = next[k]
				next[k] += uint64(1 + g.R.IntN(2))
			}
			raw, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			records = append(records, raw)
			state = state.Apply(p)
		}
		want, wantErr := foldOneRecordAtATime(header, records)
		got, gotErr := formFold(header, records)
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("seed %d: errors differ: one pass %v, per record %v", seed, gotErr, wantErr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("seed %d: one pass\n%s\nper record\n%s", seed, got, want)
		}
	}
}

func TestFormFoldOfNoRecordsIsTheWatermark(t *testing.T) {
	header := []byte(`{"a":1}`)
	got, err := formFold(header, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap, _, err := decodeFormWatermark(got)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := snap.Get("a"); !ok || string(v) != "1" {
		t.Fatalf("state = %s", got)
	}
}

// What a rotation pays: the previous watermark and a run of small records
// folded into the next watermark, one record at a time against one pass, at
// three state sizes. A full 2 MB segment holds ~10k such records; per-record
// is measured on a 200-record run (its cost is linear in records), one-pass
// on both.
func BenchmarkFormRotateFold(b *testing.B) {
	for _, size := range []int{2 << 10, 200 << 10, 2 << 20} {
		header, segment := rotationFixture(b, size, 2<<20, 200)
		runs := []struct {
			name    string
			records [][]byte
			fold    func([]byte, [][]byte) ([]byte, error)
		}{
			{"records=200/per-record", segment[:200], foldOneRecordAtATime},
			{"records=200/one-pass", segment[:200], formFold},
			{fmt.Sprintf("records=%d/one-pass", len(segment)), segment, formFold},
		}
		for _, run := range runs {
			b.Run(fmt.Sprintf("state=%dKB/%s", size>>10, run.name), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					out, err := run.fold(header, run.records)
					if err != nil {
						b.Fatal(err)
					}
					storeSink += len(out)
				}
			})
		}
	}
}

func rotationFixture(b *testing.B, stateBytes, segmentBytes, recordBytes int) ([]byte, [][]byte) {
	b.Helper()
	keys := max(stateBytes/200, 1)
	set := make(map[string]json.RawMessage, keys)
	filler := strings.Repeat("x", 180)
	for i := range keys {
		v, _ := json.Marshal(fmt.Sprintf("%06d%s", i, filler))
		set[fmt.Sprintf("k%06d", i)] = v
	}
	base := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, set, nil))
	header, err := encodeFormWatermark(base, nil)
	if err != nil {
		b.Fatal(err)
	}
	pad := strings.Repeat("y", max(recordBytes-60, 1))
	var records [][]byte
	for total := 0; total < segmentBytes; {
		i := len(records)
		v, _ := json.Marshal(fmt.Sprintf("%d%s", i, pad))
		p := form.Build(base, map[string]json.RawMessage{fmt.Sprintf("k%06d", i%keys): v}, nil)
		raw, err := json.Marshal(formRecord{Patch: p})
		if err != nil {
			b.Fatal(err)
		}
		records = append(records, raw)
		total += len(raw)
	}
	return header, records
}
