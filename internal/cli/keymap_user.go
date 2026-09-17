package cli

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/jack-work/figaro/internal/keyconf"
)

// The user layer: keys.toml, applied over the built-in table.
//
// `keymap` stays the defaults, untouched, so the frozen oracles keep
// describing what figaro ships; this pass is a pure function over them, and
// applying no rows yields exactly the default table.

// keyDiag is one row of keys.toml that did not take effect, and why.
type keyDiag struct {
	row  int // 1-based position in the file
	text string
}

func (d keyDiag) String() string { return fmt.Sprintf("key %d: %s", d.row, d.text) }

// userKeyDiags is what the last apply refused. The help pit prints a count,
// `figaro doctor keys` prints the list.
var userKeyDiags []keyDiag

// userTouchedHelp marks the help rows whose bindings the user has changed.
// Their hand-written key column is prose about the DEFAULTS, so it stops
// being usable the moment those bindings move.
var userTouchedHelp = map[helpID]bool{}

// actionByFunc resolves a row's function back to its registry entry. Function
// pointers, the same identity TestKeymap_ActionArraysAgreeWithTheIndex uses.
var actionByFunc = func() map[uintptr]*keyAction {
	m := make(map[uintptr]*keyAction, 2*len(keyActions))
	for i := range keyActions {
		a := &keyActions[i]
		if a.pager != nil {
			m[reflect.ValueOf(a.pager).Pointer()] = a
		}
		if a.input != nil {
			m[reflect.ValueOf(a.input).Pointer()] = a
		}
	}
	return m
}()

func actionFor(bd *keyBinding) *keyAction {
	switch {
	case bd.input != nil:
		return actionByFunc[reflect.ValueOf(bd.input).Pointer()]
	case bd.pager != nil:
		return actionByFunc[reflect.ValueOf(bd.pager).Pointer()]
	}
	return nil
}

// templateRow is the default row an action is normally bound on: it carries
// the open policy and the help id a user binding inherits, so a remapped key
// opens the pager exactly as the original did and appears on the same help row.
func templateRow(a *keyAction) *keyBinding {
	for i := range keymap {
		bd := &keymap[i]
		if act := actionFor(bd); act == a {
			return bd
		}
	}
	return nil
}

// applyUserKeys rebuilds the live keymap from the defaults plus these rows.
func applyUserKeys(rows []keyconf.Row) []keyDiag {
	if len(rows) == 0 {
		liveRows, userKeyDiags, userTouchedHelp = keymap, nil, map[helpID]bool{}
		buildKeyIndex()
		return nil
	}
	live := append([]keyBinding(nil), keymap...)
	var diags []keyDiag
	applied := 0
	touched := map[helpID]bool{}
	refuse := func(i int, format string, args ...any) {
		diags = append(diags, keyDiag{row: i + 1, text: fmt.Sprintf(format, args...)})
	}

	for i, r := range rows {
		spec, err := parseKeySpec(r.Chord)
		if err != nil {
			refuse(i, "%s", err)
			continue
		}
		modes, err := parseModes(r.Mode)
		if err != nil {
			refuse(i, "%s", err)
			continue
		}
		unbind := r.Action == "" || r.Action == "none"
		var act *keyAction
		if !unbind {
			act = actionByName[r.Action]
			if act == nil {
				refuse(i, "no action named %q (figaro keys lists them)", r.Action)
				continue
			}
		}
		if why := refuseChord(spec, modes, unbind); why != "" {
			refuse(i, "%s", why)
			continue
		}
		if why := refuseShadow(live, spec, modes, act, unbind); why != "" {
			refuse(i, "%s", why)
			continue
		}
		for m := keyMode(0); m < numKeyModes; m++ {
			if modes&(1<<m) == 0 {
				continue
			}
			for _, c := range spec.chords {
				live = rebindOne(live, c, m, act, touched)
				applied++
			}
		}
	}

	// Every row refused: the defaults are still exactly the defaults, and
	// saying so with the same slice keeps that provable by identity.
	if applied == 0 {
		live = keymap
	}
	liveRows = live
	buildKeyIndex()
	userKeyDiags = diags
	userTouchedHelp = touched
	return diags
}

// rebindOne clears whatever holds this chord in this mode and, unless the row
// was an unbind, installs the action in its place.
func rebindOne(live []keyBinding, c chord, m keyMode, act *keyAction, touched map[helpID]bool) []keyBinding {
	for i := range live {
		bd := &live[i]
		if bd.chord != c || bd.modes&(1<<m) == 0 {
			continue
		}
		if !bd.hidden() {
			touched[bd.help] = true
		}
		bd.modes &^= 1 << m
	}
	if act == nil {
		return live
	}
	row := keyBinding{chord: c, modes: 1 << m, open: staysInline, why: "a user binding"}
	if tmpl := templateRow(act); tmpl != nil {
		row.open, row.why, row.help = tmpl.open, tmpl.why, tmpl.help
	}
	if act.input != nil {
		row.input = act.input
	} else {
		row.pager = act.pager
	}
	// A pager action cannot run in incipit: the pager is not up. The row is
	// still legal everywhere else it was asked for.
	if row.pager != nil && m == modeIncipit {
		return live
	}
	if !row.hidden() {
		touched[row.help] = true
	}
	return append(live, row)
}

// refuseChord is the standing list: keys a reader must not be able to take
// away from themselves, and keys that are text where they are being bound.
func refuseChord(spec keySpec, modes keyModeSet, unbind bool) string {
	for _, c := range spec.chords {
		if c.kind == chordByte && c.b == 0x03 && !unbind {
			return "^C is the guaranteed way out; it cannot be rebound"
		}
		if c.kind == chordByte && c.b == 0x03 && unbind {
			return "^C is the guaranteed way out; it cannot be unbound"
		}
		for m := keyMode(0); m < numKeyModes; m++ {
			if modes&(1<<m) == 0 {
				continue
			}
			if m != modeSearch && m != modeBox {
				continue
			}
			switch {
			case c.kind == chordByte && (c.b == 0x0d || c.b == 0x0a):
				return "Enter submits the " + userModeName(m) + " box; it cannot be rebound there"
			case c.kind == chordByte && c.b == 0x1b:
				return "Esc leaves the " + userModeName(m) + " box, and is the escape prefix; it cannot be rebound there"
			case c.kind == chordByte && c.b >= 0x20 && c.b < 0x7f:
				return fmt.Sprintf("%q is literal text in the %s box", string(c.b), userModeName(m))
			}
		}
	}
	return ""
}

// refuseShadow catches the binding that would quietly never fire, or quietly
// kill the row beneath it: input rows run before pager rows (stream.go), so a
// chord claimed at one level cannot also be claimed at the other.
func refuseShadow(live []keyBinding, spec keySpec, modes keyModeSet, act *keyAction, unbind bool) string {
	if unbind {
		return ""
	}
	want := act.level()
	for i := range live {
		bd := &live[i]
		if bd.modes&modes == 0 {
			continue
		}
		var have keyLevel = levelPager
		if bd.input != nil {
			have = levelInput
		}
		if have == want {
			continue
		}
		for _, c := range spec.chords {
			if bd.chord != c {
				continue
			}
			other := actionFor(bd)
			name := "a built-in binding"
			if other != nil {
				name = other.name
			}
			if want == levelPager {
				return fmt.Sprintf("%s would never fire: %s holds that chord at the input level", spec.text, name)
			}
			return fmt.Sprintf("%s would silently kill %s, which holds that chord at the pager level", spec.text, name)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// The live map, as rows a reader can read.

type keyListing struct {
	chord  string
	modes  string
	action string
	level  string
}

func listKeys(rows []keyBinding) []keyListing {
	out := make([]keyListing, 0, len(rows))
	seen := map[keyListing]bool{}
	for i := range rows {
		bd := &rows[i]
		if bd.modes == 0 {
			continue
		}
		name := "?"
		if a := actionFor(bd); a != nil {
			name = a.name
		}
		level := "pager"
		if bd.input != nil {
			level = "input"
		}
		// The two encodings of one physical key spell the same thing and are
		// one row to a reader.
		for m := keyMode(0); m < numKeyModes; m++ {
			if bd.modes&(1<<m) == 0 {
				continue
			}
			row := keyListing{chord: bd.chord.spell(), modes: userModeName(m), action: name, level: level}
			if seen[row] {
				continue
			}
			seen[row] = true
			out = append(out, row)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].modes != out[j].modes {
			return out[i].modes < out[j].modes
		}
		return out[i].chord < out[j].chord
	})
	return out
}

func renderKeyListing(rows []keyListing) []string {
	w := 0
	for _, r := range rows {
		if len(r.chord) > w {
			w = len(r.chord)
		}
	}
	var out []string
	mode := ""
	for _, r := range rows {
		if r.modes != mode {
			mode = r.modes
			out = append(out, "", mode)
		}
		out = append(out, fmt.Sprintf("  %-*s  %s", w, r.chord, r.action))
	}
	return out
}

// helpDiagLine is the row the help pit draws when keys.toml was not wholly
// honoured. Silence here is the defect config.go measured: a setting that
// stops applying and says nothing.
func helpDiagLine() string {
	if len(userKeyDiags) == 0 {
		return ""
	}
	return fmt.Sprintf("keys.toml: %d row(s) ignored, see `figaro doctor keys`", len(userKeyDiags))
}

func diagLines() []string {
	out := make([]string, 0, len(userKeyDiags))
	for _, d := range userKeyDiags {
		out = append(out, d.String())
	}
	return out
}
