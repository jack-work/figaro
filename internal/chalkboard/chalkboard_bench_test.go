package chalkboard_test

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
)

// Benchmark the hot path: Diff a snapshot of N keys against a prior of
// N keys with one differing value. This is what the agent's
// applyChalkboardInput does on every prompt that ships a Context.
func BenchmarkSnapshot_Diff_10keys_1diff(b *testing.B) { benchSnapshotDiff(b, 10, 1) }
func BenchmarkSnapshot_Diff_50keys_1diff(b *testing.B) { benchSnapshotDiff(b, 50, 1) }
func BenchmarkSnapshot_Diff_50keys_5diff(b *testing.B) { benchSnapshotDiff(b, 50, 5) }

func benchSnapshotDiff(b *testing.B, n, diffs int) {
	prev := make(chalkboard.Snapshot, n)
	next := make(chalkboard.Snapshot, n)
	for i := 0; i < n; i++ {
		key := keyFor(i)
		prev[key] = json.RawMessage(`"value-` + key + `"`)
		if i < diffs {
			next[key] = json.RawMessage(`"changed-` + key + `"`)
		} else {
			next[key] = prev[key]
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = next.Diff(prev)
	}
}

func BenchmarkSnapshot_Apply(b *testing.B) {
	prev := chalkboard.Snapshot{
		"cwd":   json.RawMessage(`"/foo"`),
		"model": json.RawMessage(`"claude-opus"`),
		"label": json.RawMessage(`"morning"`),
	}
	patch := chalkboard.Patch{
		Set: map[string]json.RawMessage{
			"cwd":      json.RawMessage(`"/bar"`),
			"datetime": json.RawMessage(`"now"`),
		},
		Remove: []string{"label"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = prev.Apply(patch)
	}
}

func BenchmarkRender_DefaultTemplates_5entries(b *testing.B) {
	tmpls, err := chalkboard.LoadDefaultTemplates()
	if err != nil {
		b.Fatal(err)
	}
	prev := chalkboard.Snapshot{}
	patch := chalkboard.Patch{
		Set: map[string]json.RawMessage{
			"cwd":      json.RawMessage(`"/home/figaro"`),
			"root":     json.RawMessage(`"/home/figaro"`),
			"datetime": json.RawMessage(`"Wednesday, April 29, 2026, 10AM EDT"`),
			"model":    json.RawMessage(`"claude-opus-4-6"`),
			"label":    json.RawMessage(`"morning"`),
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := chalkboard.Render(patch, prev, tmpls)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func keyFor(i int) string {
	return "k" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	j := len(buf)
	for i > 0 {
		j--
		buf[j] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[j:])
}
