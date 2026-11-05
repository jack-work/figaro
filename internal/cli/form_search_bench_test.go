package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func ariasSized(n int) form.Snapshot {
	var kv []string
	words := []string{"king", "of", "spain", "steering", "lanes", "fixing", "the", "pit", "search", "keyring"}
	for i := 0; i < n; i++ {
		base := fmt.Sprintf("arias.a%04d.", i)
		mantra := fmt.Sprintf("%s %s %s %d", words[i%10], words[(i/10)%10], words[(i/100)%10], i)
		kv = append(kv,
			base+"mantra", `"`+mantra+`"`,
			base+"handle", fmt.Sprintf(`"word-%d"`, i),
			base+"cwd", `"/home/gluck/dev/figaro-qua/lane"`,
			base+"state", `"dormant"`,
			base+"parent", fmt.Sprintf(`"a%04d"`, i/7),
			base+"last_active", fmt.Sprintf("%d", 1_700_000_000_000+i),
		)
	}
	return form.FromMap(formKeys(kv...))
}

func searchStepProbe(b *testing.B, n int, query string) *transcript {
	b.Helper()
	var out strings.Builder
	tr := newTestTranscript(&out)
	tr.enter()
	v := &formView{src: &fakeFormSource{snap: ariasSized(n)}, open: map[string]bool{}}
	tr.showLivePit("arias", v, false)
	tr.drawerLines()
	tr.key('/')
	for i := 0; i < len(query); i++ {
		tr.key(query[i])
	}
	tr.drawerLines()
	return tr
}

func BenchmarkSearchStep(b *testing.B) {
	for _, n := range []int{100, 1055} {
		b.Run(fmt.Sprintf("arias=%d", n), func(b *testing.B) {
			tr := searchStepProbe(b, n, "king")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tr.searchStepHit(1)
				tr.drawerLines()
			}
		})
	}
}

type tickingSource struct {
	snaps []form.Snapshot
	at    int
}

func (s *tickingSource) Shown() form.Snapshot    { return s.snaps[s.at%len(s.snaps)] }
func (s *tickingSource) Version() uint64         { return uint64(s.at) }
func (s *tickingSource) Edit(p form.Patch) error { return nil }
func (s *tickingSource) tick()                   { s.at++ }

func BenchmarkSearchStepAfterATick(b *testing.B) {
	for _, n := range []int{1055} {
		b.Run(fmt.Sprintf("arias=%d", n), func(b *testing.B) {
			base := ariasSized(n)
			var snaps []form.Snapshot
			for i := 0; i < 4; i++ {
				snaps = append(snaps, base.Apply(form.Build(base, formKeys("arias.a0001.last_active", fmt.Sprintf("%d", i)), nil)))
			}
			src := &tickingSource{snaps: snaps}
			var out strings.Builder
			tr := newTestTranscript(&out)
			tr.enter()
			v := &formView{src: src, open: map[string]bool{}}
			tr.showLivePit("arias", v, false)
			tr.drawerLines()
			tr.key('/')
			for _, c := range []byte("king") {
				tr.key(c)
			}
			tr.drawerLines()
			b.Logf("hits: %d", len(v.search.ranked))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				src.tick()
				tr.searchStepHit(1)
				tr.drawerLines()
			}
		})
	}
}
