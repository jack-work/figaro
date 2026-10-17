package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

var updateKeymap = flag.Bool("update-keymap", false, "rewrite testdata/keymap from the current tree")

// The compiled keymap, as text: for every mode and every chord, the function
// a keystroke reaches, plus the three decoder tables the bindings steer. It is
// the equivalence oracle for any change to how bindings are DECLARED: a
// refactor of the table that leaves this file byte-identical has changed
// nothing a reader can press.
//
// It names Go functions, not actions, on purpose: an action name is part of
// the declaration being refactored, and an oracle written in the vocabulary of
// its subject cannot catch that vocabulary drifting.
func compiledKeymap() string {
	var b strings.Builder
	funcName := func(f any) string {
		n := runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
		return n[strings.LastIndex(n, ".")+1:]
	}
	named := func(f any, m keyMode, c chord, prefix string) string {
		n := funcName(f)
		if !strings.HasPrefix(n, "func") {
			return n
		}
		for i := range liveRows {
			r := &liveRows[i]
			if r.prefix == prefix && r.chord == c && r.modes&(1<<m) != 0 {
				return r.action
			}
		}
		return n
	}
	line := func(m keyMode, c chord) {
		ev := chordEvent(c)
		if f := inputAct.input(m, ev); f != nil {
			fmt.Fprintf(&b, "%-10s %-18s input %s\n", modeName(m), c, named(f, m, c, ""))
		}
		if f := pagerAct.pager(m, ev); f != nil {
			fmt.Fprintf(&b, "%-10s %-18s pager %s\n", modeName(m), c, named(f, m, c, ""))
		}
	}
	for m := keyMode(0); m < numKeyModes; m++ {
		for _, c := range everyChord() {
			line(m, c)
		}
	}
	for _, l := range liveLayers {
		if !l.prefix {
			continue
		}
		for m := keyMode(0); m < numKeyModes; m++ {
			for _, c := range everyChord() {
				bd := prefixBinding(l.name, m, chordEvent(c))
				if bd == nil {
					continue
				}
				level, f := "pager", any(bd.pager)
				if bd.input != nil {
					level, f = "input", any(bd.input)
				}
				fmt.Fprintf(&b, "%-10s %-18s %s %s\n", l.name+"+"+modeName(m), c, level, named(f, m, c, l.name))
			}
		}
	}
	for _, c := range everyChord() {
		if opensTranscript(chordEvent(c)) {
			fmt.Fprintf(&b, "opens      %s\n", c)
		}
	}
	for l := byte('a'); l <= 'z'; l++ {
		if ctrlChordLetters[l-'a'] {
			fmt.Fprintf(&b, "csi-u      ctrl-%c\n", l)
		}
	}
	for m := keyMode(0); m < numKeyModes; m++ {
		for c := 0; c < 128; c++ {
			if metaBound[m][c] {
				fmt.Fprintf(&b, "meta       %-10s %s\n", modeName(m), metaChord(byte(c)))
			}
		}
	}
	return b.String()
}

func everyChord() []chord {
	out := make([]chord, 0, 256+navCount+26+128)
	for b := 0; b < 256; b++ {
		out = append(out, byteChord(byte(b)))
	}
	for n := navUp; int(n) < navCount; n++ {
		out = append(out, navChord(n))
	}
	for l := byte('a'); l <= 'z'; l++ {
		out = append(out, ctrlChord(l))
	}
	for c := 0; c < 128; c++ {
		out = append(out, metaChord(byte(c)))
	}
	return out
}

func chordEvent(c chord) keyEvent {
	switch c.kind {
	case chordNav:
		return keyEvent{nav: c.nav}
	case chordCtrlLetter:
		return keyEvent{ctrl: c.b}
	case chordMeta:
		return keyEvent{meta: c.b}
	}
	return keyEvent{b: c.b}
}

// golden compares got against testdata/keymap/<name>, or rewrites it under
// -update-keymap. Rewriting is how a DELIBERATE change is recorded; the diff in the
// commit is the review.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "keymap", name)
	if *updateKeymap {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update-keymap to record it)", err)
	}
	if string(want) == got {
		return
	}
	gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
	var diff []string
	seen := map[string]int{}
	for _, l := range wl {
		seen[l]++
	}
	for _, l := range gl {
		if seen[l] > 0 {
			seen[l]--
			continue
		}
		diff = append(diff, "+ "+l)
	}
	for l, n := range seen {
		for ; n > 0; n-- {
			diff = append(diff, "- "+l)
		}
	}
	if len(diff) > 40 {
		diff = append(diff[:40], fmt.Sprintf("… %d more", len(diff)-40))
	}
	t.Fatalf("%s differs from the tree (-update-keymap records a deliberate change):\n%s", path, strings.Join(diff, "\n"))
}

func TestKeymapCompiledIsTheGolden(t *testing.T) {
	golden(t, "compiled.golden", compiledKeymap())
}
