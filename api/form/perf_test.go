package form_test

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
)

// Patch size against the real default bound form: what a one-field edit writes.
func BenchmarkOneFieldEdit(b *testing.B) {
	m := realBoundFormFor(b)
	bound := form.FromMap(m)

	// The largest object-valued key on the bound form.
	target, field, size := largestObjectKey(b, m)
	b.Logf("target %s.%s, value is %d bytes", target, field, size)

	b.Run("patch-bytes", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			next := bound.SetPath(target+"."+field, json.RawMessage(`"changed"`))
			p := next.Diff(bound)
			raw, err := json.Marshal(p)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(len(raw)), "patch-bytes")
		}
	})
}

func TestPatchSizeAgainstWholeValueRewrite(t *testing.T) {
	m := realBoundFormFor(t)
	bound := form.FromMap(m)
	target, field, size := largestObjectKey(t, m)

	next := bound.SetPath(target+"."+field, json.RawMessage(`"changed"`))
	p := next.Diff(bound)
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
			// The smallest field: an invertible patch carries what it displaced.
			best := -1
			for f, fv := range obj {
				if best < 0 || len(fv) < best {
					field, best = f, len(fv)
				}
			}
		}
	}
	if key == "" {
		tb.Skip("no object-valued key on the fixture bound form")
	}
	return key, field, size
}

func realBoundFormFor(tb testing.TB) map[string]json.RawMessage {
	tb.Helper()
	data, err := readFixture()
	require.NoError(tb, err)
	var m map[string]json.RawMessage
	require.NoError(tb, json.Unmarshal(data, &m))
	return m
}

func readFixture() ([]byte, error) {
	for _, p := range []string{"testdata/bound-default.json"} {
		if b, err := os.ReadFile(p); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("fixture not found")
}
