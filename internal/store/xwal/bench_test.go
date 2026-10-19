package xwal

import (
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/internal/store/disk"
	"github.com/jack-work/figaro/internal/store/segment"
)

const benchSegmentSize = 64 << 10

// benchTS is a realistic record timestamp so benchmark frames carry the
// same byte weight production frames do.
const benchTS = int64(1754850000000)

func benchReducer(_ []byte, patch []byte) ([]byte, error) {
	return append([]byte(nil), patch...), nil
}

func benchConfig() Config {
	return Config{
		Main:        "ir",
		SegmentSize: benchSegmentSize,
		Registry: map[string]Reducer{
			"latest": {Reduce: benchReducer, Initial: []byte("{}")},
		},
		Channels: []ChannelSpec{
			{Name: "ir", Kind: ChannelLog},
			{Name: "translations", Kind: ChannelLog},
			{Name: "state", Kind: ChannelReducible, Reducer: "latest"},
		},
	}
}

func buildLongAria(b *testing.B, records int) (string, Config) {
	b.Helper()
	dir := filepath.Join(b.TempDir(), "aria")
	cfg := benchConfig()
	x, err := Open(dir, cfg)
	if err != nil {
		b.Fatal(err)
	}
	if err := x.Close(); err != nil {
		b.Fatal(err)
	}

	for _, spec := range cfg.Channels {
		opts := disk.Options{
			Codec:       segment.JSONLCodec{},
			SegmentSize: cfg.SegmentSize,
		}
		if spec.Kind == ChannelReducible {
			r := cfg.Registry[spec.Reducer]
			opts.OnSegmentOpen = reducibleFold(r.Reduce, r.Initial)
		}
		l, err := disk.Open(filepath.Join(dir, spec.Name), opts)
		if err != nil {
			b.Fatal(err)
		}
		for i := 1; i <= records; i++ {
			payload := []byte(fmt.Sprintf(`{"kind":"event","seq":%d,"text":"representative agent timeline payload"}`, i))
			if spec.Name == "state" {
				payload = []byte(fmt.Sprintf(`{"turn":%d}`, i))
			}
			if err := l.Write(uint64(i), encodeFrame(uint64(i), payload, []byte(`{"source":"bench"}`), benchTS)); err != nil {
				b.Fatal(err)
			}
		}
		if err := l.Sync(); err != nil {
			b.Fatal(err)
		}
		if err := l.Close(); err != nil {
			b.Fatal(err)
		}
	}
	return dir, cfg
}

func BenchmarkXWALLongAria(b *testing.B) {
	for _, records := range []int{1_000, 10_000, 50_000} {
		b.Run(strconv.Itoa(records), func(b *testing.B) {
			dir, cfg := buildLongAria(b, records)

			b.Run("Open", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					x, err := Open(dir, cfg)
					if err != nil {
						b.Fatal(err)
					}
					if err := x.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})

			x, err := Open(dir, cfg)
			if err != nil {
				b.Fatal(err)
			}
			defer x.Close()

			b.Run("Read", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					idx := uint64(i%records + 1)
					if _, err := x.ReadAt("ir", idx); err != nil {
						b.Fatal(err)
					}
				}
			})

			b.Run("ReadFromTail256", func(b *testing.B) {
				from := uint64(1)
				if records > 256 {
					from = uint64(records - 255)
				}
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					got, err := x.RecordsFrom("ir", from, 0)
					if err != nil {
						b.Fatal(err)
					}
					if len(got) != records-int(from)+1 {
						b.Fatalf("read %d records, want %d", len(got), records-int(from)+1)
					}
				}
			})

			b.Run("ScanFromEndTail256", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					seen := 0
					err := x.channel("ir").mustLog().ScanFromEnd(uint64(records), func(uint64, []byte) error {
						seen++
						if seen == 256 {
							return errStopRange
						}
						return nil
					})
					if err != errStopRange {
						b.Fatal(err)
					}
				}
			})

			b.Run("LookupCold", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					cold, err := Open(dir, cfg)
					if err != nil {
						b.Fatal(err)
					}
					if _, ok, err := cold.Lookup("translations", uint64(records)); err != nil || !ok {
						b.Fatalf("lookup: ok=%v err=%v", ok, err)
					}
					if err := cold.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})

			b.Run("LookupColdFirst", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					cold, err := Open(dir, cfg)
					if err != nil {
						b.Fatal(err)
					}
					if _, ok, err := cold.Lookup("translations", 1); err != nil || !ok {
						b.Fatalf("lookup: ok=%v err=%v", ok, err)
					}
					if err := cold.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})

			if _, ok, err := x.Lookup("translations", uint64(records)); err != nil || !ok {
				b.Fatalf("prime lookup: ok=%v err=%v", ok, err)
			}
			b.Run("LookupHot", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, ok, err := x.Lookup("translations", uint64(i%records+1)); err != nil || !ok {
						b.Fatalf("lookup: ok=%v err=%v", ok, err)
					}
				}
			})

			b.Run("StateAt", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := x.StateAt("state", uint64(records)); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

func buildLongTrunk(b *testing.B, records int) (*Trunks, Config, TrunkID, uint64) {
	b.Helper()
	dir := filepath.Join(b.TempDir(), "tree")
	cfg := benchConfig()
	trunks, err := createTrunks(dir, cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := trunks.Close(); err != nil {
			b.Error(err)
		}
	})
	trunk, err := trunks.SpawnUnderRoot()
	if err != nil {
		b.Fatal(err)
	}
	// Built through the real append path, so the fixture is a store shape
	// production writes.
	for i := 1; i <= records; i++ {
		payload := fmt.Appendf(nil, `{"kind":"event","seq":%d,"text":"representative agent timeline payload"}`, i)
		_, lt, err := trunks.Append(trunk, 0, payload, []byte(`{"source":"bench"}`))
		if err != nil {
			b.Fatal(err)
		}
		if _, err := trunks.AppendChannel(trunk, "translations", lt, payload, []byte(`{"source":"bench"}`)); err != nil {
			b.Fatal(err)
		}
		if _, err := trunks.AppendChannel(trunk, "state", lt, fmt.Appendf(nil, `{"turn":%d}`, i), []byte(`{"source":"bench"}`)); err != nil {
			b.Fatal(err)
		}
	}
	// Seal memory to disk so the open-shaped subbenches measure segments,
	// not the flusher's backlog.
	x, err := trunks.Head(trunk)
	if err != nil {
		b.Fatal(err)
	}
	if err := x.SyncCoherent(); err != nil {
		b.Fatal(err)
	}
	if err := x.Close(); err != nil {
		b.Fatal(err)
	}
	// Root genesis at LT 1, then `records` own appends: global tail.
	return trunks, cfg, trunk, uint64(records) + 1
}

func BenchmarkTrunksLongAria(b *testing.B) {
	for _, records := range []int{1_000, 10_000, 50_000} {
		b.Run(strconv.Itoa(records), func(b *testing.B) {
			trunks, _, trunk, tail := buildLongTrunk(b, records)

			b.Run("HeadOpenReadTip", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					x, err := trunks.Head(trunk)
					if err != nil {
						b.Fatal(err)
					}
					if _, err := x.ReadAt("ir", tail); err != nil {
						b.Fatal(err)
					}
					if err := x.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})

			b.Run("List", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if got := trunks.List(); len(got) != 1 || got[0].Tip != tail {
						b.Fatalf("List = %+v", got)
					}
				}
			})

			b.Run("ListLight", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if got := trunks.ListLight(); len(got) != 1 {
						b.Fatalf("ListLight = %+v", got)
					}
				}
			})

			other, err := trunks.ForkTail(trunk)
			if err != nil {
				b.Fatal(err)
			}
			// The fixture mutates under the append subbenches: every append
			// grows the tail the next append pays for, so the scratch trunk is
			// re-forked every rebuildEvery appends with the timer stopped,
			// pinning the tail regardless of benchtime.
			const rebuildEvery = 256
			b.Run("Append", func(b *testing.B) {
				b.ReportAllocs()
				scratch := other
				var err error
				for i := 0; i < b.N; i++ {
					if i%rebuildEvery == 0 && i > 0 {
						b.StopTimer()
						scratch, err = trunks.ForkTail(trunk)
						if err != nil {
							b.Fatal(err)
						}
						b.StartTimer()
					}
					if _, _, err := trunks.Append(scratch, 0, []byte(`{"kind":"event"}`), nil); err != nil {
						b.Fatal(err)
					}
				}
			})

			// Interleaved appends across two lineages: the cross-trunk overhead
			// relative to the single-trunk Append above. Not RunParallel, since
			// parallel goroutines cannot pause the timer to re-pin the fixture.
			b.Run("AppendTwoTrunks", func(b *testing.B) {
				b.StopTimer()
				pair := [2]TrunkID{}
				repair := func() {
					for j := range pair {
						p, err := trunks.ForkTail(trunk)
						if err != nil {
							b.Fatal(err)
						}
						pair[j] = p
					}
				}
				repair()
				b.ReportAllocs()
				b.StartTimer()
				for i := 0; i < b.N; i++ {
					if i%rebuildEvery == 0 && i > 0 {
						b.StopTimer()
						repair()
						b.StartTimer()
					}
					if _, _, err := trunks.Append(pair[i&1], 0, []byte(`{"kind":"event"}`), nil); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
