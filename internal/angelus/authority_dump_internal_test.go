package angelus

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
)

// P6: every authority fact a store yields, one line per node and one per
// descent, in a fixed order, so two builds reading copies of one store can
// be compared byte for byte. FIGARO_P6_STORE names the arias directory of a
// COPY (opening repairs it); FIGARO_P6_OUT is the file written.
func TestAuthorityDump(t *testing.T) {
	src, out := os.Getenv("FIGARO_P6_STORE"), os.Getenv("FIGARO_P6_OUT")
	if src == "" || out == "" {
		t.Skip("FIGARO_P6_STORE names a copy of a store, FIGARO_P6_OUT the dump")
	}
	backend, err := store.NewXwalBackend(src, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	facts := StoreFacts(backend)
	a := &Angelus{Backend: backend}
	a.Policies = authz.NewPolicies(jkauthzOwner(), facts)
	if err := a.Policies.Reload(""); err != nil {
		t.Fatal(err)
	}
	h := &handlers{angelus: a}
	chains := facts.(authz.FigaroFacts)

	nodes := backend.Nodes()
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.New()
	w := bufio.NewWriter(f)
	line := func(format string, args ...any) {
		s := fmt.Sprintf(format, args...)
		w.WriteString(s)
		sum.Write([]byte(s))
	}
	for _, n := range nodes {
		chain, held := chains.Ancestry(n.ID)
		eff, holds := a.Policies.Effective(n.ID)
		writers := h.ancestorsOf(lineage{of: n.ID})
		line("node %s kind=%s held=%v chain=%s holds=%v effective=%q writers=%s\n",
			n.ID, n.Kind, held, strings.Join(chain, ","), holds, eff.String(), strings.Join(writers, ","))
	}
	descents := 0
	for _, x := range nodes {
		for _, y := range nodes {
			if x.ID != y.ID && facts.DescendsFrom(x.ID, y.ID) {
				line("under %s %s\n", x.ID, y.ID)
				descents++
			}
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("P6 dump: nodes=%d descents=%d sha256=%x", len(nodes), descents, sum.Sum(nil))
}
