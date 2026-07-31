package cli

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/internal/term"
)

// applyAmbiguousWidth decides how wide ─ and │ are, and it asks the terminal
// rather than guessing.
//
// Precedence, and each step exists for a reason:
//
//	FIGARO_AMBIGUOUS_WIDE   an explicit answer always wins. Someone who knows
//	                        their terminal should never be argued with, and it
//	                        is the escape hatch when the probe is wrong.
//	a cached answer         keyed by $TERM: the probe costs a round trip to the
//	                        terminal, and the answer cannot change for a given
//	                        terminal type. Probing on every invocation would put
//	                        an escape sequence and a read in front of `figaro
//	                        list` forever.
//	the probe               draw ─, ask where the cursor landed, believe it.
//	nothing                 no tty, no answer, or an unparseable one: keep the
//	                        default. A guess here would be wrong on every
//	                        terminal instead of on the unusual one.
func applyAmbiguousWidth() {
	if v := strings.TrimSpace(os.Getenv("FIGARO_AMBIGUOUS_WIDE")); v != "" {
		runewidth.DefaultCondition.EastAsianWidth = envTruthy(v)
		return
	}
	if wide, ok := cachedAmbiguousWide(); ok {
		runewidth.DefaultCondition.EastAsianWidth = wide
		return
	}
	wide, ok := term.ProbeAmbiguousWide(120 * time.Millisecond)
	if !ok {
		return
	}
	runewidth.DefaultCondition.EastAsianWidth = wide
	storeAmbiguousWide(wide)
}

// ambiWidthCachePath is per-$TERM: the answer is a property of the terminal
// program, not of this run.
func ambiWidthCachePath() string {
	t := strings.TrimSpace(os.Getenv("TERM"))
	if t == "" {
		return ""
	}
	dir := cacheDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "ambiwidth-"+strings.ReplaceAll(t, "/", "_"))
}

func cachedAmbiguousWide() (bool, bool) {
	p := ambiWidthCachePath()
	if p == "" {
		return false, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false, false
	}
	switch strings.TrimSpace(string(b)) {
	case "wide":
		return true, true
	case "narrow":
		return false, true
	}
	return false, false
}

func storeAmbiguousWide(wide bool) {
	p := ambiWidthCachePath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	v := "narrow"
	if wide {
		v = "wide"
	}
	_ = os.WriteFile(p, []byte(v+"\n"), 0o600)
}
