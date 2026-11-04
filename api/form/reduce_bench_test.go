package form_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

type reduceShape struct {
	name  string
	base  form.Snapshot
	edits [2]form.Patch
}

func leafValue(i, size int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("v%d-%s", i, strings.Repeat("x", size))))
}

func wideShape(name string, keys int) reduceShape {
	m := map[string]json.RawMessage{}
	for i := 0; i < keys; i++ {
		m[fmt.Sprintf("k%05d", i)] = leafValue(i, 24)
	}
	base := form.FromMap(m)
	key := fmt.Sprintf("k%05d", keys/2)
	return reduceShape{name: name, base: base, edits: [2]form.Patch{
		form.Build(base, map[string]json.RawMessage{key: leafValue(-1, 24)}, nil),
		form.Build(base, map[string]json.RawMessage{key: leafValue(-2, 24)}, nil),
	}}
}

func deepShape() reduceShape {
	m := map[string]json.RawMessage{}
	var walk func(prefix string, depth int)
	walk = func(prefix string, depth int) {
		for i := 0; i < 4; i++ {
			p := fmt.Sprintf("%sn%d", prefix, i)
			if depth == 6 {
				m[p] = leafValue(i, 24)
				continue
			}
			walk(p+".", depth+1)
		}
	}
	walk("", 1)
	base := form.FromMap(m)
	key := "n1.n2.n3.n0.n1.n2"
	return reduceShape{name: "deep4096", base: base, edits: [2]form.Patch{
		form.Build(base, map[string]json.RawMessage{key: leafValue(-1, 24)}, nil),
		form.Build(base, map[string]json.RawMessage{key: leafValue(-2, 24)}, nil),
	}}
}

func dressShape() reduceShape {
	m := map[string]json.RawMessage{}
	for i := 0; i < 100; i++ {
		m[fmt.Sprintf("system.k%03d", i)] = leafValue(i, 24)
	}
	base := form.FromMap(m)
	dress := func(gen int) form.Patch {
		set := map[string]json.RawMessage{}
		for i := 0; i < 500; i++ {
			set[fmt.Sprintf("skills.s%03d.body", i)] = leafValue(i*10+gen, 4096)
		}
		return form.Build(base, set, nil)
	}
	return reduceShape{name: "dress500x4k", base: base, edits: [2]form.Patch{dress(1), dress(2)}}
}

func smallShape() reduceShape {
	s := wideShape("small10", 10)
	return s
}

func reduceShapes() []reduceShape {
	return []reduceShape{smallShape(), wideShape("wide1k", 1000), wideShape("wide5k", 5000), deepShape(), dressShape()}
}

func BenchmarkReduce(b *testing.B) {
	for _, sh := range reduceShapes() {
		b.Run(sh.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, c := reduceForBench(sh.base, sh.edits[i&1])
				if c.IsIdentity() {
					b.Fatal("an edit reduced to nothing")
				}
			}
		})
	}
}

func BenchmarkApplyShapes(b *testing.B) {
	for _, sh := range reduceShapes() {
		b.Run(sh.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = sh.base.Apply(sh.edits[i&1])
			}
		})
	}
}

func BenchmarkDiffShapes(b *testing.B) {
	for _, sh := range reduceShapes() {
		next := sh.base.Apply(sh.edits[0])
		b.Run(sh.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if next.Diff(sh.base).IsIdentity() {
					b.Fatal("no difference")
				}
			}
		})
	}
}
