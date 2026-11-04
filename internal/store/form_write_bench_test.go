package store_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

func writeBenchValue(i, size int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("v%d-%s", i, strings.Repeat("x", size))))
}

func writeBenchForm(b *testing.B, base map[string]json.RawMessage) *store.Form {
	b.Helper()
	f, err := store.OpenForm(&store.MemFormLog{})
	if err != nil {
		b.Fatal(err)
	}
	if _, err := f.Apply(form.Build(form.Snapshot{}, base, nil), 0); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(f.Close)
	return f
}

func BenchmarkFormWrite(b *testing.B) {
	wide := func(keys int) map[string]json.RawMessage {
		m := map[string]json.RawMessage{}
		for i := 0; i < keys; i++ {
			m[fmt.Sprintf("k%05d", i)] = writeBenchValue(i, 24)
		}
		return m
	}
	oneKey := func(key string) func(i int) message.Patch {
		return func(i int) message.Patch {
			return form.Build(form.Snapshot{}, map[string]json.RawMessage{key: writeBenchValue(i, 24)}, nil)
		}
	}
	dress := func(i int) message.Patch {
		set := map[string]json.RawMessage{}
		for j := 0; j < 500; j++ {
			set[fmt.Sprintf("skills.s%03d.body", j)] = writeBenchValue(i*1000+j, 4096)
		}
		return form.Build(form.Snapshot{}, set, nil)
	}
	cases := []struct {
		name  string
		base  map[string]json.RawMessage
		write func(i int) message.Patch
	}{
		{"small10", wide(10), oneKey("k00005")},
		{"wide1k", wide(1000), oneKey("k00500")},
		{"wide5k", wide(5000), oneKey("k02500")},
		{"dress500x4k", wide(100), dress},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			f := writeBenchForm(b, c.base)
			patches := [2]message.Patch{c.write(1), c.write(2)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := f.Apply(patches[i&1], 0); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
