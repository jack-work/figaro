package cli

import (
	"fmt"
	"math/rand"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
)

// stressLine builds one markdown line of at least target runes from a palette
// mixing ASCII, accented latin, wide CJK, emoji, and inline markdown styling.
func stressLine(rng *rand.Rand, lt, target int) string {
	palette := []string{
		"lorem", "ipsum", "dolor", "xylophone", "**bold**", "`code`", "_ital_",
		"héllo", "straße", "漢字文化圏", "→←", "🎼🎻", "naïve", "→x←",
		fmt.Sprintf("m%03d", lt),
	}
	var sb strings.Builder
	count := 0
	for count < target {
		word := palette[rng.Intn(len(palette))]
		if count > 0 {
			sb.WriteByte(' ')
			count++
		}
		sb.WriteString(word)
		count += len([]rune(word))
	}
	return sb.String()
}

// stressHistory scripts n committed messages: 1-3 prose lines of 30-80 runes
// each, a styled tool node every 7th message, and periodic long 'a' runs so
// the pathological-regex probe has real fuel.
func stressHistory(rng *rand.Rand, n int) []aria.Committed {
	out := make([]aria.Committed, n)
	for i := range out {
		lt := i + 1
		role := "assistant"
		if lt%4 == 1 {
			role = "user"
		}
		var sb strings.Builder
		for l := range 1 + rng.Intn(2) {
			if l > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(stressLine(rng, lt, 30+rng.Intn(51)))
		}
		nodes := []livedoc.Node{{Type: livedoc.NodeProse, Markdown: sb.String()}}
		if lt%7 == 0 {
			nodes = append(nodes, livedoc.Node{
				Type: livedoc.NodeTool, Name: "bash", Status: "ok",
				Summary:   fmt.Sprintf("run step %d — αβγ 漢字", lt),
				Output:    strings.Repeat(fmt.Sprintf("out %d line αβγ 漢字 wide\n", lt), 2),
				StartedAt: 1700000000, FinishedAt: 1700000002,
			})
		}
		if lt%97 == 0 {
			nodes = append(nodes, livedoc.Node{
				Type: livedoc.NodeProse, Markdown: strings.Repeat("a", 140),
			})
		}
		out[i] = aria.Committed{LT: lt, Role: role, Nodes: nodes}
	}
	return out
}

// TestTranscriptStress hammers a big transcript window (600 styled committed
// messages plus a streaming open message) with 20k random keypresses from the
// full key alphabet, periodic live-frame applies and resizes, the structural
// invariants after every op, and a loose 60s wall-clock bound that catches
// quadratic blowups. Part (b) probes RE2's linear-time guarantee with
// classically pathological patterns.
// stressBudget: the race detector slows execution 5-15x — overhead, not a
// regression signal.
var stressBudget = func() time.Duration {
	if raceEnabled {
		return 6 * time.Minute
	}
	return time.Minute
}()

func TestTranscriptStress(t *testing.T) {
	if testing.Short() {
		t.Skip("stress harness: skipped with -short")
	}
	rng := rand.New(rand.NewSource(20260721))
	history := stressHistory(rng, 600)
	w := newFzWorldHistory(history, 100, 30, &ariaView{settings: &renderSettings{}})
	step := func(op int, desc string, fn func()) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic at op %d (%s): %v\n%s", op, desc, r, debug.Stack())
			}
		}()
		fn()
	}

	alphabet := []byte("/?&\r\x1b\x7f jkhlvVwbe^$gGnNyiq0123456789abcdfmopstxz")
	for b := byte(0x01); b <= 0x1f; b++ {
		alphabet = append(alphabet, b)
	}

	start := time.Now()
	const ops = 20000
	for op := range ops {
		b := alphabet[rng.Intn(len(alphabet))]
		step(op, fmt.Sprintf("key %q", b), func() { w.tr.key(b) })
		step(op, "page-service", w.servePages)
		if op%500 == 250 {
			step(op, "live-delta", w.liveDelta)
		}
		if op%500 == 499 {
			rw, rh := 20+rng.Intn(101), 5+rng.Intn(36)
			step(op, fmt.Sprintf("resize %dx%d", rw, rh), func() { w.tr.resize(rw, rh) })
		}
		// Field invariants every op; the rendering-heavy structural checks on a
		// cadence (and right after every mutation) so 20k ops stay inside the
		// wall-clock bound without thinning coverage where it matters.
		step(op, "invariants", func() {
			if op%5 == 0 || op%500 == 250 || op%500 == 499 {
				w.checkInvariants(t, fmt.Sprintf("op %d key %q", op, b))
			} else {
				w.checkCheap(t, fmt.Sprintf("op %d key %q", op, b))
			}
		})
		if op%1000 == 999 && time.Since(start) > stressBudget {
			t.Fatalf("stress run exceeded %v at op %d (%v) — quadratic blowup?", stressBudget, op, time.Since(start))
		}
	}
	if el := time.Since(start); el > stressBudget {
		t.Fatalf("%d keypresses took %v (> %v) — quadratic blowup?", ops, el, stressBudget)
	}

	// Normalize out of whatever mode the random walk ended in: cancel an open
	// prompt, drop visual/cursor mode, :noh, follow the tail.
	step(-1, "normalize", func() {
		if w.tr.inSearch {
			w.tr.key(0x1b)
		}
		w.tr.key('q')
		w.tr.key(0x1b)
		w.tr.key('G')
	})

	// (b) pathological-regex probe: RE2 is linear-time, so committing these and
	// walking their matches must stay fast. x{0,50}{0,50} does not even
	// compile (nested repetition) — the prompt must absorb it just as quickly.
	for _, pat := range []string{`(a+)+$`, `.*.*.*x`, `x{0,50}{0,50}`} {
		st := time.Now()
		step(-1, "probe: open prompt", func() { w.tr.key('/') })
		for i := 0; i < len(pat); i++ {
			c := pat[i]
			step(-1, fmt.Sprintf("probe: type %q", c), func() { w.tr.key(c) })
		}
		step(-1, "probe: commit", func() { w.tr.key('\r') })
		step(-1, "probe: page-service", w.servePages)
		for range 3 {
			step(-1, "probe: n", func() { w.tr.key('n') })
			step(-1, "probe: page-service", w.servePages)
		}
		step(-1, "probe: unwind", func() {
			if w.tr.inSearch {
				w.tr.key(0x1b) // bad pattern held the prompt open
			}
			w.tr.key(0x1b) // cursor mode -> off
			w.tr.key(0x1b) // :noh
		})
		step(-1, "probe: invariants", func() { w.checkInvariants(t, "probe "+pat) })
		if el := time.Since(st); el > 2*time.Second {
			t.Fatalf("pathological pattern %q took %v (> 2s) — search engine not linear-time?", pat, el)
		}
	}
}
