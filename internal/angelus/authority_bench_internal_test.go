package angelus

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
)

// The authority reads every RPC of a lineage-scoped caller makes, over a copy
// of a real store (FIGARO_BENCH_SNAPSHOT, FIGARO_BENCH_SCRATCH as the
// Snapshot benchmarks). One op is one read (DescendsFrom: one each way,
// so a true and a false answer); the reads cycle through the 64
// conversations with the longest ancestry, each against the far end of its
// own chain.
//
//	go test ./internal/angelus -run '^$' -bench 'AuthorityRead' -count=10
func BenchmarkAuthorityRead(b *testing.B) {
	src := os.Getenv("FIGARO_BENCH_SNAPSHOT")
	if src == "" {
		b.Skip("FIGARO_BENCH_SNAPSHOT names the store to copy")
	}
	scratch := os.Getenv("FIGARO_BENCH_SCRATCH")
	if scratch == "" {
		scratch = filepath.Dir(src)
	}
	dir, err := os.MkdirTemp(scratch, "authority-bench-")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })
	arias := filepath.Join(dir, "arias")
	if out, err := exec.Command("cp", "-a", "--reflink=auto", filepath.Join(src, "arias"), arias).CombinedOutput(); err != nil {
		b.Fatalf("copy %s: %v: %s", src, err, out)
	}
	backend, err := store.NewXwalBackend(arias, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { backend.Close() })
	facts := StoreFacts(backend)
	policies := authz.NewPolicies(jkauthzOwner(), facts)
	if err := policies.Reload(""); err != nil {
		b.Fatal(err)
	}
	chains := facts.(authz.FigaroFacts)

	type pair struct {
		id, far string
		depth   int
	}
	var deep []pair
	for _, n := range backend.Nodes() {
		chain, held := chains.Ancestry(n.ID)
		if !held || len(chain) < 2 {
			continue
		}
		far := chain[0]
		if far == n.ID {
			far = chain[len(chain)-1]
		}
		deep = append(deep, pair{n.ID, far, len(chain)})
	}
	sort.Slice(deep, func(i, j int) bool {
		if deep[i].depth != deep[j].depth {
			return deep[i].depth > deep[j].depth
		}
		return deep[i].id < deep[j].id
	})
	if len(deep) > 64 {
		deep = deep[:64]
	}
	if len(deep) == 0 {
		b.Fatal("no conversation in the store has an ancestor")
	}
	b.Logf("%d conversations, ancestry %d to %d", len(deep), deep[len(deep)-1].depth, deep[0].depth)

	b.Run("DescendsFrom", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			p := deep[i%len(deep)]
			facts.DescendsFrom(p.id, p.far)
			facts.DescendsFrom(p.far, p.id)
			i++
		}
	})
	b.Run("Ancestry", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			chains.Ancestry(deep[i%len(deep)].id)
			i++
		}
	})
	b.Run("Effective", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			policies.Effective(deep[i%len(deep)].id)
			i++
		}
	})
}
