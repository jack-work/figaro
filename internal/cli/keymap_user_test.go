package cli

import (
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/keyconf"
)

func withUserKeys(t *testing.T, f keyconf.File) []compileDiag {
	t.Helper()
	t.Cleanup(func() { applyUserKeys(keyconf.File{}) })
	return applyUserKeys(f)
}

func binds(layer string, kv ...string) keyconf.File {
	var f keyconf.File
	for i := 0; i+1 < len(kv); i += 2 {
		f.Set(layer, kv[i], kv[i+1])
	}
	return f
}

func reached(m keyMode, c chord) string {
	ev := chordEvent(c)
	for i := range liveRows {
		r := &liveRows[i]
		if r.chord == c && r.modes&(1<<m) != 0 {
			return r.action
		}
	}
	if inputAct.input(m, ev) != nil || pagerAct.pager(m, ev) != nil {
		return "?"
	}
	return ""
}

// The spelling the help pit prints is the spelling keys.toml accepts.
func TestKeySpell_RoundTripsEveryRow(t *testing.T) {
	for i := range keymap {
		c := keymap[i].chord
		spec, err := parseKeySpec(c.spell())
		if err != nil {
			t.Errorf("%s spells %q, which does not parse: %s", c, c.spell(), err)
			continue
		}
		found := false
		for _, got := range spec.chords {
			found = found || got == c
		}
		if !found {
			t.Errorf("%s spells %q, which parses to %v", c, c.spell(), spec.chords)
		}
	}
}

// Every action is bound somewhere by default, and every default binding
// resolves. An action nobody can reach is either dead or forgotten, and the
// defaults compiling with a diagnostic would be a panic at init.
func TestDefaultLayers_EveryActionIsBound(t *testing.T) {
	used := map[string]bool{}
	for i := range keymap {
		used[keymap[i].action] = true
	}
	for _, a := range keyActions {
		if !used[a.name] {
			t.Errorf("action %q is bound nowhere by default", a.name)
		}
	}
	if _, diags := compileLayers(defaultLayers); len(diags) > 0 {
		t.Fatalf("the defaults compile with diagnostics: %v", diags)
	}
}

// A polymorphic action is ONE binding: j is bound once, in the motions layer,
// and means the right thing in every mode that layer covers.
func TestOneBindingMeansTheRightThingPerMode(t *testing.T) {
	j := byteChord('j')
	for m, want := range map[keyMode]string{
		modeTranscript: "pagerLineDown",
		modeVisual:     "pagerVisualDown",
		modeComposer:   "composerDownLine",
		modeList:       "listDown",
	} {
		if got := actionNameOfFunc(pagerAct.pager(m, chordEvent(j))); got != want {
			t.Errorf("j in %s reaches %s, want %s", modeName(m), got, want)
		}
	}
	n := 0
	for _, l := range defaultLayers {
		if l.prefix {
			continue
		}
		for _, b := range l.binds {
			for _, k := range splitKeys(b.keys) {
				if k == "j" {
					n++
				}
			}
		}
	}
	if n != 1 {
		t.Fatalf("j is declared %d times; the point of a polymorphic action is once", n)
	}
}

// A binding claims a key only where its action means something. Motions'
// 'h' is "left", which the transcript has no use for, so the transcript's 'h'
// (help) must show through from the layer below.
func TestABindingClaimsOnlyWhereItsActionIsImplemented(t *testing.T) {
	// The user layer sits above everything and covers every mode, so its
	// 'h' would hide the transcript's if a binding claimed a key where its
	// action does nothing.
	if diags := withUserKeys(t, binds(userLayerName, "h", "left")); len(diags) > 0 {
		t.Fatal(diags)
	}
	if got := reached(modeTranscript, byteChord('h')); got != "help" {
		t.Fatalf("h in transcript reaches %q, want help", got)
	}
	if got := reached(modeVisual, byteChord('h')); got != "left" {
		t.Fatalf("h in visual reaches %q, want left", got)
	}
}

func TestUserRebindReplacesTheDefaultInItsLayer(t *testing.T) {
	if diags := withUserKeys(t, binds("motions", "J", "half-down", "d", "")); len(diags) > 0 {
		t.Fatal(diags)
	}
	for _, m := range []keyMode{modeTranscript, modeVisual} {
		if got := reached(m, byteChord('J')); got != "half-down" {
			t.Errorf("J in %s reaches %q", modeName(m), got)
		}
		if got := reached(m, byteChord('d')); got != "" {
			t.Errorf("d in %s still reaches %q after an unbind", modeName(m), got)
		}
	}
	// Nothing else moved.
	if got := reached(modeTranscript, byteChord('u')); got != "half-up" {
		t.Errorf("u reaches %q", got)
	}
}

// The user layer sits above every other and covers every mode, and still only
// claims where its action is implemented: binding z to sticky must not take z
// from the command line, where it is a letter.
func TestTheUserLayerIsOnTopAndPolite(t *testing.T) {
	if diags := withUserKeys(t, binds(userLayerName, "z", "sticky", "x", "help")); len(diags) > 0 {
		t.Fatal(diags)
	}
	if got := reached(modeTranscript, byteChord('z')); got != "sticky" {
		t.Errorf("z in transcript reaches %q", got)
	}
	if got := reached(modeBox, byteChord('z')); got != "" {
		t.Errorf("z in the command line reaches %q; it must stay text", got)
	}
	// Above the transcript layer, which binds x to delete.
	if got := reached(modeTranscript, byteChord('x')); got != "help" {
		t.Errorf("x in transcript reaches %q, want the user's help", got)
	}
}

func TestUserLayerDeclaredAboveAnother(t *testing.T) {
	f := keyconf.File{Layer: map[string]keyconf.Layer{
		"mine": {Modes: []string{"visual"}, Above: "motions", Bind: map[string]string{"j": "half-down"}},
	}}
	if diags := withUserKeys(t, f); len(diags) > 0 {
		t.Fatal(diags)
	}
	if got := reached(modeVisual, byteChord('j')); got != "half-down" {
		t.Errorf("j in visual reaches %q", got)
	}
	if got := reached(modeTranscript, byteChord('j')); got != "down" {
		t.Errorf("j in transcript reaches %q; mine covers visual only", got)
	}
	if i, m := layerIndex(liveLayers, "mine"), layerIndex(liveLayers, "motions"); i < 0 || i+1 != m {
		t.Fatalf("mine at %d, motions at %d: above means directly above", i, m)
	}
}

// A refused binding says why, by name, and leaves the defaults in force.
func TestRefusals(t *testing.T) {
	cases := []struct {
		f    keyconf.File
		want string
	}{
		{binds("session", "0x03", "listen"), "^C is the guaranteed way out"},
		{binds(userLayerName, "^C", ""), "^C is the guaranteed way out"},
		{binds("command", "Esc", "cmd-home"), "Esc leaves the command box"},
		{binds(userLayerName, "Enter", ""), "Enter submits the"},
		{binds("command", "q", "cmd-home"), `"q" is literal text in the command box`},
		{binds("motions", "j", "no-such-thing"), `no action named "no-such-thing"`},
		{binds("nowhere", "j", "down"), "no layer named nowhere"},
		{binds("motions", "^M", "down"), "Enter's byte"},
		{binds("pager", "z", "cmd-home"), "does nothing in any mode this layer applies to"},
		{keyconf.File{Layer: map[string]keyconf.Layer{"motions": {Modes: []string{"visual"}}}}, "already exists"},
		{keyconf.File{Layer: map[string]keyconf.Layer{"mine": {Modes: []string{"nope"}}}}, `unknown mode "nope"`},
	}
	defaults := compiledKeymap()
	for _, c := range cases {
		diags := withUserKeys(t, c.f)
		joined := ""
		for _, d := range diags {
			joined += d.String() + "\n"
		}
		if !strings.Contains(joined, c.want) {
			t.Errorf("%+v: want a refusal saying %q, got:\n%s", c.f, c.want, joined)
		}
		if compiledKeymap() != defaults {
			t.Errorf("%+v: a refused binding changed the live map", c.f)
		}
		applyUserKeys(keyconf.File{})
	}
}

// An empty file is the identity, by pointer: nothing was rebuilt.
func TestNoUserKeysIsTheDefaultTable(t *testing.T) {
	withUserKeys(t, keyconf.File{})
	if &liveRows[0] != &keymap[0] {
		t.Fatal("an empty keys.toml rebuilt the table")
	}
}

// The help panel's key column follows a remap: prose about the defaults is
// honoured only while nothing under its row has moved.
func TestHelpColumnFollowsARemap(t *testing.T) {
	withUserKeys(t, binds("motions", "j", "", "J", "down"))
	body := strings.Join(helpBody(), "\n")
	if strings.Contains(body, "j/k · u/d · gg/G") {
		t.Fatalf("the help pit still prints the default column for a remapped row:\n%s", body)
	}
	if !strings.Contains(body, "J") {
		t.Fatalf("the help pit does not show the new key:\n%s", body)
	}
}

func TestKeysTomlEndToEnd(t *testing.T) {
	path := t.TempDir() + "/keys.toml"
	if err := keyconf.Save(path, binds("motions", "J", "half-down")); err != nil {
		t.Fatal(err)
	}
	f, warnings, err := keyconf.Load(path)
	if err != nil || len(warnings) > 0 {
		t.Fatal(err, warnings)
	}
	if diags := withUserKeys(t, f); len(diags) > 0 {
		t.Fatal(diags)
	}
	if got := reached(modeVisual, byteChord('J')); got != "half-down" {
		t.Fatalf("J in visual reaches %q", got)
	}
}

func actionNameOfFunc(f pagerFunc) string {
	if f == nil {
		return ""
	}
	n := runtimeFuncName(reflect.ValueOf(f).Pointer())
	return n[strings.LastIndex(n, ".")+1:]
}

func runtimeFuncName(pc uintptr) string { return runtime.FuncForPC(pc).Name() }

// gg is a prefix layer, and "top" means the right thing wherever g arms it.
func TestGGIsAPrefixInEveryMotionMode(t *testing.T) {
	for m, want := range map[keyMode]string{
		modeTranscript: "pagerTop",
		modeVisual:     "pagerVisualTop",
		modeComposer:   "composerBufferStart",
		modeList:       "listTop",
	} {
		bd := prefixBinding("g", m, keyEvent{b: 'g'})
		if bd == nil {
			t.Errorf("g g reaches nothing in %s", modeName(m))
			continue
		}
		if got := actionNameOfFunc(bd.pager); got != want {
			t.Errorf("g g in %s reaches %s, want %s", modeName(m), got, want)
		}
	}
}

// A pending gesture is spent by the next key, whatever it is: a key the
// prefix does not bind is itself, and the gesture is gone.
func TestAnyKeySpendsAPendingGesture(t *testing.T) {
	tr := oracleTranscript()
	tr.key('g')
	if tr.pending != "g" {
		t.Fatalf("g left %q pending", tr.pending)
	}
	tr.key('h')
	if tr.pending != "" {
		t.Fatalf("h left %q pending", tr.pending)
	}
	if !tr.showing("help") {
		t.Fatal("the key that spent the gesture must still act as itself")
	}
}

// The leader is a layer a reader can fill: Space h opens help because the
// file says so, and the default Space n is untouched.
func TestUserLeaderChord(t *testing.T) {
	if diags := withUserKeys(t, binds("leader", "h", "help")); len(diags) > 0 {
		t.Fatal(diags)
	}
	tr := oracleTranscript()
	tr.key(' ')
	tr.key('h')
	if !tr.showing("help") {
		t.Fatal("Space h did not open help")
	}
	if bd := prefixBinding("leader", modeTranscript, keyEvent{b: 'n'}); bd == nil || bd.action != "notifications" {
		t.Fatalf("Space n moved: %+v", bd)
	}
}

// A reader can declare a prefix of their own and arm it from any layer.
func TestUserDefinedPrefix(t *testing.T) {
	f := keyconf.File{Layer: map[string]keyconf.Layer{
		"z": {Modes: []string{"transcript"}, Prefix: true, Bind: map[string]string{"h": "help"}},
	}}
	f.Set("transcript", "z", "prefix:z")
	if diags := withUserKeys(t, f); len(diags) > 0 {
		t.Fatal(diags)
	}
	tr := oracleTranscript()
	tr.key('z')
	if tr.pending != "z" {
		t.Fatalf("z left %q pending", tr.pending)
	}
	tr.key('h')
	if !tr.showing("help") {
		t.Fatal("z h did not open help")
	}
}

func TestAPrefixCannotArmAnother(t *testing.T) {
	diags := withUserKeys(t, binds("leader", "g", "prefix:g"))
	if len(diags) == 0 || !strings.Contains(diags[0].String(), "cannot arm another prefix") {
		t.Fatalf("want a refusal, got %v", diags)
	}
}
