package form_test

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

// The numbers that justify the change, against the real default board.
//
// The headline is patch SIZE: what a one-field edit writes to the form channel
// and re-renders into a turn, since that cost is paid on every change forever.
func BenchmarkOneFieldEdit(b *testing.B) {
	m := realBoardFor(b)
	board := form.FromMap(m)

	// The largest object-valued key on the board: what a flat patch rewrote
	// whole to change one field.
	target, field, size := largestObjectKey(b, m)
	b.Logf("target %s.%s, value is %d bytes", target, field, size)

	b.Run("patch-bytes", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			next := board.SetPath(target+"."+field, json.RawMessage(`"changed"`))
			p := next.Diff(board)
			raw, err := json.Marshal(p)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(len(raw)), "patch-bytes")
		}
	})
}

// A one-field edit, measured as bytes, against the whole value a flat patch
// would have carried.
func TestPatchSizeAgainstWholeValueRewrite(t *testing.T) {
	m := realBoardFor(t)
	board := form.FromMap(m)
	target, field, size := largestObjectKey(t, m)

	next := board.SetPath(target+"."+field, json.RawMessage(`"changed"`))
	p := next.Diff(board)
	raw, err := json.Marshal(p)
	require.NoError(t, err)

	t.Logf("key %s holds %d bytes; a one-field patch is %d bytes (%.1fx smaller)",
		target, size, len(raw), float64(size)/float64(len(raw)))
	require.Less(t, len(raw), size/2,
		"a one-field edit must not carry the whole value")
}

func largestObjectKey(tb testing.TB, m map[string]json.RawMessage) (key, field string, size int) {
	tb.Helper()
	for k, v := range m {
		var obj map[string]json.RawMessage
		if json.Unmarshal(v, &obj) != nil || len(obj) == 0 {
			continue
		}
		if len(v) > size {
			key, size = k, len(v)
			// The SMALLEST field: changing a large one must carry the large
			// one, because an invertible patch holds what it displaced.
			best := -1
			for f, fv := range obj {
				if best < 0 || len(fv) < best {
					field, best = f, len(fv)
				}
			}
		}
	}
	if key == "" {
		tb.Skip("no object-valued key on the fixture board")
	}
	return key, field, size
}

func realBoardFor(tb testing.TB) map[string]json.RawMessage {
	tb.Helper()
	data, err := readFixture()
	require.NoError(tb, err)
	var m map[string]json.RawMessage
	require.NoError(tb, json.Unmarshal(data, &m))
	return m
}

func readFixture() ([]byte, error) {
	for _, p := range []string{"testdata/board-default.json"} {
		if b, err := os.ReadFile(p); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("fixture not found")
}
