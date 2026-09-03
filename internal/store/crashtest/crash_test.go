package crashtest

import (
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var (
	seedFlag   = flag.Int64("seed", 0, "root seed; 0 = time-based")
	longFlag   = flag.Bool("long", false, "run hundreds of kill cycles")
	cyclesFlag = flag.Int("cycles", 0, "override total cycle count")
)

func TestMain(m *testing.M) {
	if os.Getenv("FIGWAL_CRASH_CHILD") == "1" {
		runChild()
		return
	}
	if os.Getenv("FIGWAL_CRASH_SLOG") != "" {
		f, _ := os.OpenFile(os.Getenv("FIGWAL_CRASH_SLOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	os.Exit(m.Run())
}

// testSeed picks the root seed. IT IS FIXED BY DEFAULT, and that is a choice
// about what a gate is for: this harness kills a child mid-write and checks
// what survived, so a time-based seed makes every push a fresh lottery -- the
// same commit passed on main and failed on release, which teaches nobody
// anything and trains everybody to re-run the job.
//
// So: deterministic here, random where randomness pays. FIGWAL_CRASH_RANDOM=1
// (or -seed) explores; a scheduled run that sets it is worth more than a
// flaky one that blocks a merge.
func testSeed(t *testing.T) (int64, int64) {
	root := *seedFlag
	if root == 0 {
		root = crashSeed
		if os.Getenv("FIGWAL_CRASH_RANDOM") != "" {
			root = time.Now().UnixNano()
		}
	}
	h := fnv.New64a()
	io.WriteString(h, t.Name())
	return root, root ^ int64(h.Sum64())
}

// crashSeed is the fixed root. Any value works; this one is a date.
const crashSeed = 20260903

func cycleCounts() (stores, perStore int) {
	stores, perStore = 2, 6
	if *longFlag {
		stores, perStore = 15, 20
	}
	if *cyclesFlag > 0 {
		stores = (*cyclesFlag + perStore - 1) / perStore
	}
	return stores, perStore
}

var keepOnce sync.Once

func keepStore(t *testing.T, dir, ctx string) {
	keep := os.Getenv("FIGWAL_CRASH_KEEP")
	if keep == "" {
		return
	}
	keepOnce.Do(func() {
		dst := filepath.Join(keep, "violation-store")
		if err := os.CopyFS(dst, os.DirFS(dir)); err != nil {
			t.Logf("keepStore: %v", err)
			return
		}
		os.WriteFile(filepath.Join(dst, "CTX.txt"), []byte(ctx+"\n"), 0o644)
		t.Logf("kept violating store at %s (%s)", dst, ctx)
	})
}

func report(t *testing.T, gaps map[string]bool, ctx string, vs []violation) {
	t.Helper()
	shown := map[string]int{}
	for _, v := range vs {
		shown[v.class]++
		if shown[v.class] > 3 {
			continue
		}
		if gaps[v.class] {
			t.Logf("FINDING %s: %s", ctx, v)
		} else {
			t.Errorf("VIOLATION %s: %s", ctx, v)
		}
	}
	for class, n := range shown {
		if n > 3 {
			if gaps[class] {
				t.Logf("FINDING %s: [%s] ... %d more", ctx, class, n-3)
			} else {
				t.Errorf("VIOLATION %s: [%s] ... %d more", ctx, class, n-3)
			}
		}
	}
}

func runCycles(t *testing.T, md verifyMode) {
	root, seed := testSeed(t)
	t.Logf("seed=%d (replay: go test ./crashtest -run %s -seed=%d)", seed, t.Name(), root)
	rng := rand.New(rand.NewSource(seed))
	salt := fmt.Sprintf("%d", root)
	gaps := storeGaps(md)
	stores, perStore := cycleCounts()
	total := 0
	for si := 0; si < stores; si++ {
		dir := filepath.Join(t.TempDir(), fmt.Sprintf("store%d", si))
		st, _, err := createStore(dir)
		if err != nil {
			t.Fatalf("seed=%d store=%d create: %v", seed, si, err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("seed=%d store=%d close: %v", seed, si, err)
		}
		m, vs := reconcile(dir, salt, md)
		report(t, gaps, fmt.Sprintf("seed=%d store=%d baseline", seed, si), vs)
		wrecked := false
		for ci := 0; ci < perStore; ci++ {
			total++
			childSeed := rng.Int63()
			delay := killDelay(rng)
			ctx := fmt.Sprintf("seed=%d store=%d cycle=%d childSeed=%d delay=%s", seed, si, ci, childSeed, delay)
			cycleStart := time.Now()
			cutoff, err := runChildOnce(dir, childSeed, salt, delay, m)
			if err != nil {
				keepStore(t, dir, ctx+" child-error")
				if wrecked {
					// THIS store has had a segment deliberately overwritten.
					// Prefix sharing means a child is exactly as sound as its
					// parent's prefix, so a child that cannot run is the
					// damage we caused. Only then; every other child error,
					// in either mode, is still a defect.
					t.Logf("%s: child cannot run against the wrecked store: %v", ctx, err)
					break
				}
				t.Fatalf("%s: %v", ctx, err)
			}
			if m.closedClean {
				ctx += " cleanclose"
			} else if m.closeBegunStamp > 0 {
				ctx += " midclose"
			}
			if md == modeCorrupt {
				hit, err := corruptTail(rng, dir, cycleStart)
				if err != nil {
					t.Fatalf("%s: corrupt: %v", ctx, err)
				}
				if len(hit) > 0 {
					wrecked = true
				}
				ctx = fmt.Sprintf("%s corrupted=%v", ctx, hit)
			}
			midFork := len(m.pendingFork) > 0
			if midFork {
				t.Logf("%s midfork", ctx)
			}
			g := gaps
			if midFork {
				g = map[string]bool{}
				for c := range gaps {
					g[c] = true
				}
				for _, c := range forkWreckGaps() {
					g[c] = true
				}
			}
			cycleVs := verify(dir, m, salt, md, cutoff)
			report(t, g, ctx, cycleVs)
			m, vs = reconcile(dir, salt, md)
			report(t, g, ctx+" reconcile", vs)
			for _, v := range append(append([]violation(nil), cycleVs...), vs...) {
				if !g[v.class] {
					keepStore(t, dir, ctx+" "+v.class)
					break
				}
			}
			bricked := midFork && len(cycleVs)+len(vs) > 0
			for _, v := range append(vs, cycleVs...) {
				if v.class == vOpenFailed || md == modeCorrupt {
					bricked = true
				}
			}
			if bricked {
				t.Logf("%s: store damaged beyond this cycle, abandoning it", ctx)
				break
			}
		}
	}
	t.Logf("completed %d kill cycles (mode=%d)", total, md)
}

func TestCrashKill(t *testing.T) {
	runCycles(t, modeKill)
}

func TestCrashCorruptTail(t *testing.T) {
	runCycles(t, modeCorrupt)
}
