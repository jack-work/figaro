// sweepcost times what a daemon boot's background sweeps actually do, on a
// COPY of a real store. Read-only: it calls AuditLibrettos (the reconcile
// walk without its writes) and the read half of metaBackfill.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
)

// counter tallies slog records by message so each phase can report how many
// logs it opened and how many segments it recovered.
type counter struct {
	mu   sync.Mutex
	n    map[string]int
	fam  map[string]int
	root string
}

func (c *counter) Enabled(context.Context, slog.Level) bool { return true }
func (c *counter) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.n[r.Message]++
	if r.Message == "log opened" {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "dir" {
				parts := strings.Split(strings.TrimPrefix(a.Value.String(), c.root+"/"), "/")
				fam := parts[0]
				if len(parts) > 1 && (fam == "translations-v2" || fam == "form") {
					fam = fam + "/" + parts[1]
				}
				c.fam[fam]++
				return false
			}
			return true
		})
	}
	c.mu.Unlock()
	return nil
}
func (c *counter) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *counter) WithGroup(string) slog.Handler      { return c }

var tally = &counter{n: map[string]int{}, fam: map[string]int{}}

// take returns the counts since the last call and resets.
func take() string {
	tally.mu.Lock()
	defer tally.mu.Unlock()
	s := fmt.Sprintf("opens %5d  segments %5d", tally.n["log opened"], tally.n["segment recovered"])
	if len(tally.fam) > 0 {
		type kv struct {
			k string
			v int
		}
		var fs []kv
		for k, v := range tally.fam {
			fs = append(fs, kv{k, v})
		}
		sort.Slice(fs, func(i, j int) bool { return fs[i].v > fs[j].v })
		s += "  ["
		for i, f := range fs {
			if i == 4 {
				break
			}
			s += fmt.Sprintf(" %s=%d", f.k, f.v)
		}
		s += " ]"
	}
	tally.n, tally.fam = map[string]int{}, map[string]int{}
	return s
}

func main() {
	slog.SetDefault(slog.New(tally))
	root := os.Args[1]
	tally.root = root
	pf, _ := os.Create("/var/tmp/open.pprof")
	pprof.StartCPUProfile(pf)
	t := time.Now()
	b, err := store.NewXwalBackend(root, 2<<20)
	if err != nil {
		panic(err)
	}
	fmt.Printf("  NewXwalBackend                   %8.1f ms   %s\n", ms(t), take())
	t = time.Now()
	if err := wire.Install(b.Store(), root, wire.Capabilities{Trunks: true}); err != nil {
		panic(err)
	}
	fmt.Printf("  wire.Install                     %8.1f ms   %s\n", ms(t), take())
	pprof.StopCPUProfile()
	pf.Close()
	t = time.Now()

	t = time.Now()
	nodes := b.Nodes()
	fmt.Printf("Nodes()                            %8.1f ms   %s  (%d nodes)\n", ms(t), take(), len(nodes))

	t = time.Now()
	ids := b.ConversationIDs()
	fmt.Printf("ConversationIDs()                  %8.1f ms   %s  (%d arias)\n", ms(t), take(), len(ids))

	// --- the reconcile sweep's dominant half: FormState for every node.
	t = time.Now()
	boards, studies, slowest := 0, 0, time.Duration(0)
	var slowID string
	for _, n := range nodes {
		s := time.Now()
		snap, err := b.FormState(n.ID)
		d := time.Since(s)
		if d > slowest {
			slowest, slowID = d, n.ID
		}
		if err != nil {
			continue
		}
		boards++
		if _, ok := snap.Get(store.StudiesKey); ok {
			studies++
		}
	}
	total := ms(t)
	fmt.Printf("FormState() over every node        %8.1f ms   %s  (%d boards read, %d name a study, %.2f ms/board avg, worst %.1f ms %s)\n",
		total, take(), boards, studies, total/float64(max(boards, 1)), float64(slowest.Microseconds())/1000, slowID)

	// --- the whole audit, as the daemon runs it.
	t = time.Now()
	audit, err := b.AuditLibrettos()
	if err != nil {
		fmt.Println("audit:", err)
	}
	fmt.Printf("AuditLibrettos() (the boot sweep)  %8.1f ms   %s  (boards %d, librettos %d, would-correct %d, orphaned %d, missing %d)\n",
		ms(t), take(), audit.Boards, audit.Librettos, audit.Corrected, audit.Orphaned, audit.Missing)

	// --- metaBackfill's read half: Meta for every aria, FormState only for
	// sidecars still missing identity fields.
	t = time.Now()
	need, haveMeta := 0, 0
	for _, id := range ids {
		meta, err := b.Meta(id)
		if err != nil || meta == nil {
			continue
		}
		haveMeta++
		if meta.Mantra == "" && meta.OutfitName == "" && meta.Cwd == "" {
			need++
		}
	}
	fmt.Printf("metaBackfill read half             %8.1f ms   %s  (%d sidecars, %d would still fold a form)\n",
		ms(t), take(), haveMeta, need)

	// --- what a `fig ls`/`status` list pays per dormant aria.
	t = time.Now()
	for _, id := range ids {
		_, _ = b.Meta(id)
		_ = b.LastTS(id)
	}
	lt := ms(t)
	fmt.Printf("list enrichment (Meta+LastTS)      %8.1f ms   (%.3f ms/aria, single-threaded; the daemon uses 8 workers)\n",
		lt, lt/float64(max(len(ids), 1)))
}

func ms(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }
