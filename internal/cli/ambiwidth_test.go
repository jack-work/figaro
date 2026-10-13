package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mattn/go-runewidth"
)

// Explicit answer first, then the per-$TERM cache, then the probe.
func TestAmbiguousWidthPrecedence(t *testing.T) {
	restore := runewidth.DefaultCondition.EastAsianWidth
	t.Cleanup(func() { runewidth.DefaultCondition.EastAsianWidth = restore })

	dir := t.TempDir()
	t.Setenv("FIGARO_CACHE_DIR", dir)
	t.Setenv("TERM", "test-term")

	// 1. Explicit env wins, and does not write a cache entry.
	t.Setenv("FIGARO_AMBIGUOUS_WIDE", "1")
	runewidth.DefaultCondition.EastAsianWidth = false
	applyAmbiguousWidth()
	if !runewidth.DefaultCondition.EastAsianWidth {
		t.Fatal("explicit FIGARO_AMBIGUOUS_WIDE=1 was not honoured")
	}
	if _, err := os.Stat(filepath.Join(dir, "ambiwidth-test-term")); err == nil {
		t.Fatal("an explicit answer must not be cached as if it were probed")
	}

	// 2. A cached answer is used when there is no explicit one.
	t.Setenv("FIGARO_AMBIGUOUS_WIDE", "")
	storeAmbiguousWide(true)
	runewidth.DefaultCondition.EastAsianWidth = false
	applyAmbiguousWidth()
	if !runewidth.DefaultCondition.EastAsianWidth {
		t.Fatal("cached 'wide' was not honoured")
	}
	storeAmbiguousWide(false)
	runewidth.DefaultCondition.EastAsianWidth = true
	applyAmbiguousWidth()
	if runewidth.DefaultCondition.EastAsianWidth {
		t.Fatal("cached 'narrow' was not honoured")
	}
}

func TestAmbiguousWidthWithoutATTYKeepsTheDefault(t *testing.T) {
	restore := runewidth.DefaultCondition.EastAsianWidth
	t.Cleanup(func() { runewidth.DefaultCondition.EastAsianWidth = restore })

	t.Setenv("FIGARO_CACHE_DIR", t.TempDir())
	t.Setenv("TERM", "no-tty-term")
	t.Setenv("FIGARO_AMBIGUOUS_WIDE", "")

	runewidth.DefaultCondition.EastAsianWidth = false
	applyAmbiguousWidth()
	if runewidth.DefaultCondition.EastAsianWidth {
		t.Fatal("probe with no tty must not change the width table")
	}
}
