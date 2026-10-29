package cli

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func benchFormSnapshot(b *testing.B, path string) form.Snapshot {
	b.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		b.Skipf("no fixture %s: %v", path, err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		b.Fatal(err)
	}
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, keys, nil))
}

// One paint of the form pit: what every keystroke and every delta costs.
func BenchmarkFormViewItems(b *testing.B) {
	for _, shape := range []string{"wide", "nested", "deep", "huge"} {
		b.Run(shape, func(b *testing.B) {
			snap := benchFormSnapshot(b, "/tmp/bf-"+shape+".json")
			m := &formMirror{}
			m.reset(snap, 1)
			v := &formView{mirror: m, open: map[string]bool{}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				v.Items(160)
			}
		})
	}
}

func BenchmarkFormTreeBuild(b *testing.B) {
	for _, shape := range []string{"wide", "nested", "deep", "huge"} {
		snap := benchFormSnapshot(b, "/tmp/bf-"+shape+".json")
		b.Run(shape+"/remarshal", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				buildFormTree(snap)
			}
		})
		b.Run(shape+"/leaves", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				treeFromLeaves(snap)
			}
		})
	}
}

func treeFromLeaves(snap form.Snapshot) *formNode {
	root := &formNode{label: "form"}
	index := map[string]*formNode{"": root}
	for path, raw := range snap.All() {
		parent := root
		start := 0
		for i := 0; i <= len(path); i++ {
			if i < len(path) && path[i] != '.' {
				continue
			}
			p := path[:i]
			n, ok := index[p]
			if !ok {
				n = &formNode{path: p, label: path[start:i], depth: parent.depth + 1}
				index[p] = n
				parent.children = append(parent.children, n)
			}
			parent, start = n, i+1
		}
		parent.value = raw
	}
	sortFormTree(root)
	return root
}
