package cli

import (
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// THE BEHAVIOUR GOLDENS: what every key does in every starting state, swept
// through the pager's dispatch and through the real input loop.
//
// A cell is recorded only when it differs from the state's INERT signature,
// which is what 0x00 leaves (no table has ever bound it). So a key absent from
// a state's block is still an assertion: it must do nothing.
//
// The pager block names the action each cell reached, so a REBIND reads in
// the diff as the same action and the same signature under a different key,
// and a BEHAVIOUR change reads as a new signature under the same action. Both
// are recorded with -update-keymap; the commit diff is the review.

func stateNames[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func pagerSweepNames() []string {
	names := make([]string, 0, 128+int(navEnd-navUp)+1)
	for b := 0; b < 128; b++ {
		names = append(names, fmt.Sprintf("0x%02x", b))
	}
	for n := navUp; n <= navEnd; n++ {
		names = append(names, navName(n))
	}
	return names
}

func pagerSweepChord(name string) chord {
	for n := navUp; n <= navEnd; n++ {
		if navName(n) == name {
			return navChord(n)
		}
	}
	var b byte
	fmt.Sscanf(name, "0x%02x", &b)
	return byteChord(b)
}

// actionAt names what a chord reaches in a mode: the action, or the Go
// function when the registry has no name for it, or "-" when nothing is bound
// and the cell is the mode's own fallback (literal text, a dismissal).
func actionAt(m keyMode, c chord) string {
	ev := chordEvent(c)
	if f := inputAct.input(m, ev); f != nil {
		return actionNameOf(f)
	}
	if f := pagerAct.pager(m, ev); f != nil {
		return actionNameOf(f)
	}
	return "-"
}

func pagerBehaviour() string {
	var b strings.Builder
	for _, state := range stateNames(oracleStates) {
		setup := oracleStates[state]
		tr := oracleTranscript()
		setup(tr)
		mode := tr.mode()
		cells, _ := sweepPager(setup)
		inert := cells["0x00"]
		fmt.Fprintf(&b, "%s inert %s\n", state, inert)
		for _, name := range pagerSweepNames() {
			if cells[name] == inert {
				continue
			}
			fmt.Fprintf(&b, "%s %s %s %s\n", state, name, actionAt(mode, pagerSweepChord(name)), cells[name])
		}
	}
	return b.String()
}

func inputBehaviour(t *testing.T) string {
	var b strings.Builder
	for _, state := range stateNames(inputStates) {
		cells, _ := sweepInput(t, inputStates[state])
		inert := cells["0x00"]
		fmt.Fprintf(&b, "%s inert %s\n", state, inert)
		for _, k := range inputSweepKeys() {
			if cells[k.name] == inert {
				continue
			}
			fmt.Fprintf(&b, "%s %q %s\n", state, k.name, cells[k.name])
		}
	}
	return b.String()
}

func TestKeymapPagerBehaviourIsTheGolden(t *testing.T) {
	golden(t, "pager.golden", pagerBehaviour())
}

func TestKeymapInputBehaviourIsTheGolden(t *testing.T) {
	golden(t, "input.golden", inputBehaviour(t))
}

// Every pager-level binding must be reachable from some recorded state, or a
// binding could break with nothing in the goldens to say so.
func TestKeymapGoldenStatesCoverEveryMode(t *testing.T) {
	covered := map[keyMode]bool{}
	for _, state := range stateNames(oracleStates) {
		tr := oracleTranscript()
		oracleStates[state](tr)
		covered[tr.mode()] = true
	}
	for i := range keymap {
		bd := &keymap[i]
		if bd.pager == nil {
			continue
		}
		for m := keyMode(0); m < numKeyModes; m++ {
			if bd.modes&(1<<m) != 0 && !covered[m] {
				t.Errorf("%s is bound in %s, and no recorded state is in that mode", bd.chord, modeName(m))
			}
		}
	}
}

func actionNameOf(f any) string {
	if a := actionByFunc[reflect.ValueOf(f).Pointer()]; a != nil {
		return a.name
	}
	n := runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
	return n[strings.LastIndex(n, ".")+1:]
}
