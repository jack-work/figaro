package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// What the drainer pays per commit to answer the sinks' key questions, for a
// canonical patch of n leaves of size bytes, the shape a dress writes.
func BenchmarkCommitSinkChecks(b *testing.B) {
	for _, c := range []struct {
		name        string
		leaves, kib int
	}{{"1leaf", 1, 0}, {"50leaves_1KB", 50, 1}, {"500leaves_4KB", 500, 4}} {
		b.Run(c.name, func(b *testing.B) {
			p := dressShapedPatch(c.leaves, c.kib)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = namesOutfit(p)
				_, _ = targetOf(p)
				_, _ = lifetimeOf("bench", p)
			}
		})
	}
}

// One write through a Form: judge, reduce, append, publish, on a small form
// and on one holding 500 leaves of 4 KB.
func BenchmarkFormWrite(b *testing.B) {
	for _, c := range []struct {
		name        string
		leaves, kib int
	}{{"small", 4, 0}, {"big", 500, 4}} {
		b.Run(c.name, func(b *testing.B) {
			f, err := OpenForm(&MemFormLog{})
			if err != nil {
				b.Fatal(err)
			}
			defer f.Close()
			if _, err := f.Apply(dressShapedPatch(c.leaves, c.kib), 0); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p := form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(fmt.Sprintf("%q", fmt.Sprint(i)))}, nil)
				if _, err := f.Apply(p, 0); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func dressShapedPatch(leaves, kib int) message.Patch {
	body := strings.Repeat("x", kib*1024)
	set := map[string]json.RawMessage{
		keyOutfitName:      json.RawMessage(`"house"`),
		"system.model":     json.RawMessage(`"opus"`),
		SystemTTLKey:       json.RawMessage(`"7d"`),
		SystemKeepaliveKey: json.RawMessage(`"1d"`),
	}
	for i := 0; i < leaves; i++ {
		raw, _ := json.Marshal(body)
		set[fmt.Sprintf("skills.s%03d.body", i)] = raw
	}
	return form.Build(form.Snapshot{}, set, nil)
}
