package fuzzy

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

var benchWords = strings.Fields("aria form outfit skill credo system tools model provider thinking effort budget cache marker reminder renderer tags mantra cwd keepalive ttl sandbox chat receipt troupe policy door study comment thread passage figaro herald hush keyring session turn reaped topology models catalog gateway anthropic copilot responses openai deepseek rou2 version watermark segment xwal replica seq origin capability")

func benchForm(n int) form.Snapshot {
	r := rand.New(rand.NewPCG(1, 2))
	word := func() string { return benchWords[r.IntN(len(benchWords))] }
	body := func(k int) string {
		var b strings.Builder
		for i := 0; i < k; i++ {
			b.WriteString(word())
			b.WriteByte(' ')
		}
		return b.String()
	}
	m := map[string]json.RawMessage{}
	put := func(k string, v any) {
		raw, _ := json.Marshal(v)
		m[k] = raw
	}
	for i := 0; len(m) < n; i++ {
		switch i % 5 {
		case 0:
			put(fmt.Sprintf("skills.%s%d.frontmatter", word(), i), body(60))
			put(fmt.Sprintf("skills.%s%d.filePath", word(), i), "/home/gluck/.config/figaro/skills/"+word()+".md")
		case 1:
			put(fmt.Sprintf("comments.c%x.thread.t%x.text", i, r.Uint32()), body(40))
		case 2:
			put(fmt.Sprintf("system.%s.%s%d", word(), word(), i), body(3))
		case 3:
			put(fmt.Sprintf("models.%s.%s%d.context", word(), word(), i), r.IntN(1000000))
		case 4:
			put(fmt.Sprintf("notes.n%d", i), body(400))
		}
	}
	return form.FromMap(m)
}

var benchQueries = []string{"sysmo", "systools", "thinkeff", "skilfront", "keepalive", "deepseek", "credo", "watrmrk"}

func BenchmarkQuery(b *testing.B) {
	for _, n := range []int{1500, 20000} {
		var ix Index
		ix.Update(benchForm(n))
		m := NewMatcher()
		b.Run(fmt.Sprintf("leaves=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ix.Query(m, benchQueries[i%len(benchQueries)])
			}
		})
	}
}

func BenchmarkTypingAQuery(b *testing.B) {
	var ix Index
	ix.Update(benchForm(20000))
	m := NewMatcher()
	q := "systhinkeff"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for k := 1; k <= len(q); k++ {
			ix.Query(m, q[:k])
		}
	}
}

func BenchmarkUpdate(b *testing.B) {
	snap := benchForm(20000)
	b.Run("build", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var ix Index
			ix.Update(snap)
		}
	})
	b.Run("one-key-delta", func(b *testing.B) {
		var ix Index
		ix.Update(snap)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			next := snap.Apply(form.Build(snap, map[string]json.RawMessage{"system.model": json.RawMessage(fmt.Sprintf("%d", i))}, nil))
			ix.Update(next)
		}
	})
}

// The memory a 20k-leaf index holds, logged so a change to the value cap
// shows its cost.
func TestIndexMemoryAt20k(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a large form")
	}
	snap := benchForm(20000)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var ix Index
	ix.Update(snap)
	runtime.GC()
	runtime.ReadMemStats(&after)
	held := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("20k-leaf index holds about %d KB", held/1024)
	if held > 10<<20 {
		t.Fatalf("the index holds %d MB for 20k leaves; the value cap is not doing its job", held>>20)
	}
	runtime.KeepAlive(&ix)
}
