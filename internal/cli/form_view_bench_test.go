package cli

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

type benchShape struct {
	name string
	keys map[string]json.RawMessage
}

func benchWord(r *rand.Rand) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 3+r.IntN(8))
	for i := range b {
		b[i] = letters[r.IntN(len(letters))]
	}
	return string(b)
}

func benchSentence(r *rand.Rand, words int) json.RawMessage {
	parts := make([]string, words)
	for i := range parts {
		parts[i] = benchWord(r)
	}
	raw, _ := json.Marshal(strings.Join(parts, " "))
	return raw
}

func benchDeep(r *rand.Rand, keys map[string]json.RawMessage, prefix string, depth, fan int) {
	for i := range fan {
		path := fmt.Sprintf("%sn%d", prefix, i)
		if depth == 1 {
			keys[path] = benchSentence(r, 6)
			continue
		}
		benchDeep(r, keys, path+".", depth-1, fan)
	}
}

// Seeded, so before and after measure the same bytes.
func benchShapes() []benchShape {
	r := rand.New(rand.NewPCG(1, 2))
	wide := map[string]json.RawMessage{}
	for i := range 5000 {
		wide[fmt.Sprintf("k%05d", i)] = benchSentence(r, 6)
	}
	nested := map[string]json.RawMessage{}
	for i := range 50 {
		for j := range 100 {
			nested[fmt.Sprintf("g%02d.k%03d", i, j)] = benchSentence(r, 6)
		}
	}
	deep := map[string]json.RawMessage{}
	benchDeep(r, deep, "", 7, 4)
	huge := map[string]json.RawMessage{}
	for i := range 20 {
		huge[fmt.Sprintf("blob%02d", i)] = benchSentence(r, 7000)
	}
	return []benchShape{{"wide", wide}, {"nested", nested}, {"deep", deep}, {"huge", huge}}
}

func benchSnap(keys map[string]json.RawMessage) form.Snapshot {
	return form.FromMap(keys)
}

func benchTouch(keys map[string]json.RawMessage) string {
	best := ""
	for k := range keys {
		if best == "" || k < best {
			best = k
		}
	}
	return best
}

func benchPaintPit(b *testing.B, v itemView) *pit {
	b.Helper()
	var d pit
	d.showLive(pitID("form show"), v.(interface {
		itemView
		Close()
	}))
	d.lines(160, 40)
	return &d
}

// A keystroke changes nothing the view reads: what does a repaint cost?
func BenchmarkFormViewRepaint(b *testing.B) {
	for _, s := range benchShapes() {
		snap := benchSnap(s.keys)
		for _, open := range []string{"collapsed", "openall"} {
			b.Run(s.name+"/"+open, func(b *testing.B) {
				v, _ := newBenchFormView(snap)
				if open == "openall" {
					benchOpenAll(v)
				}
				d := benchPaintPit(b, v)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					d.moveSelection(1)
					d.lines(160, 40)
				}
			})
		}
	}
}

// One delta from the server, then the paint it causes.
func BenchmarkFormViewDelta(b *testing.B) {
	for _, s := range benchShapes() {
		snap := benchSnap(s.keys)
		key := benchTouch(s.keys)
		for _, open := range []string{"collapsed", "openall"} {
			b.Run(s.name+"/"+open, func(b *testing.B) {
				v, apply := newBenchFormView(snap)
				if open == "openall" {
					benchOpenAll(v)
				}
				d := benchPaintPit(b, v)
				cur := snap
				b.ReportAllocs()
				b.ResetTimer()
				for i := range b.N {
					p := form.Build(cur, map[string]json.RawMessage{key: json.RawMessage(fmt.Sprintf(`"v%d"`, i))}, nil)
					cur = cur.Apply(p)
					apply(p)
					d.lines(160, 40)
				}
			})
		}
	}
}

// Opening the pit on a form already in hand: build and first paint.
func BenchmarkFormViewOpen(b *testing.B) {
	for _, s := range benchShapes() {
		snap := benchSnap(s.keys)
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				v, _ := newBenchFormView(snap)
				benchPaintPit(b, v)
			}
		})
	}
}

var _ = rpc.FormDeltaSchema
