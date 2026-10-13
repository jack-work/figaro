package cli

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/internal/term"
)

// Decides whether ambiguous-width runes (─ │) are two cells, by asking the terminal.
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

// Per-$TERM: the answer is a property of the terminal program.
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
