package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jack-work/figaro/internal/keyconf"
)

const userLayerName = "user"

var (
	liveLayers      = defaultLayers
	userKeyDiags    []compileDiag
	userTouchedHelp = map[helpID]bool{}
)

func applyUserKeys(f keyconf.File) []compileDiag {
	if !f.HasBindings() {
		liveLayers, liveRows, userKeyDiags, userTouchedHelp = defaultLayers, keymap, nil, map[helpID]bool{}
		buildKeyIndex()
		return nil
	}
	layers, diags := overlayLayers(defaultLayers, f)
	rows, cdiags := compileLayers(layers)
	diags = append(diags, cdiags...)
	liveLayers, liveRows = layers, rows
	userKeyDiags = diags
	userTouchedHelp = touchedHelp(keymap, rows)
	buildKeyIndex()
	return diags
}

func overlayLayers(base []keyLayer, f keyconf.File) ([]keyLayer, []compileDiag) {
	layers := make([]keyLayer, len(base))
	for i, l := range base {
		layers[i] = keyLayer{name: l.name, modes: l.modes, prefix: l.prefix, binds: append([]keyBind(nil), l.binds...)}
	}
	var diags []compileDiag
	refuse := func(layer, keys, format string, args ...any) {
		diags = append(diags, compileDiag{layer: layer, keys: keys, text: fmt.Sprintf(format, args...)})
	}

	for _, name := range sortedNames(f.Layer) {
		spec := f.Layer[name]
		if layerByName(layers, name) != nil {
			refuse(name, "", "a layer named %s already exists; rebind its keys under [bind.%s]", name, name)
			continue
		}
		if len(spec.Modes) == 0 {
			refuse(name, "", "a layer needs modes, as modes = [\"transcript\"]")
			continue
		}
		modes, err := parseModes(strings.Join(spec.Modes, ","))
		if err != nil {
			refuse(name, "", "%s", err)
			continue
		}
		at := 0
		if spec.Above != "" {
			i := layerIndex(layers, spec.Above)
			if i < 0 {
				refuse(name, "", "above: no layer named %s", spec.Above)
				continue
			}
			at = i
		}
		l := keyLayer{name: name, modes: modes, prefix: spec.Prefix}
		l.binds = userBinds(&l, spec.Bind, refuse)
		layers = append(layers[:at], append([]keyLayer{l}, layers[at:]...)...)
	}

	for _, name := range sortedNames(f.Bind) {
		l := layerByName(layers, name)
		if l == nil && name == userLayerName {
			layers = append([]keyLayer{{name: userLayerName, modes: inEverywhere}}, layers...)
			l = &layers[0]
		}
		if l == nil {
			refuse(name, "", "no layer named %s (figaro keys lists them)", name)
			continue
		}
		l.binds = append(userBinds(l, f.Bind[name], refuse), l.binds...)
	}
	return layers, diags
}

func userBinds(l *keyLayer, m map[string]string, refuse func(layer, keys, format string, args ...any)) []keyBind {
	var out []keyBind
	for _, keys := range sortedNames(m) {
		action := m[keys]
		if action == "none" {
			action = ""
		}
		if why := refuseUserBind(l, keys, action); why != "" {
			refuse(l.name, keys, "%s", why)
			continue
		}
		out = append(out, keyBind{keys: keys, action: action, help: defaultHelpOf(action)})
	}
	return out
}

func refuseUserBind(l *keyLayer, keys, action string) string {
	var a *keyAction
	if _, isCommand := boundCommand(action); isCommand {
		if a = commandBinding(action); a == nil {
			return "a command binding names a command after the colon, as \":toggle system.methods.enabled\""
		}
	} else if action != "" && !strings.HasPrefix(action, prefixAction) {
		if a = actionByName[action]; a == nil {
			return fmt.Sprintf("no action named %q (figaro keys actions lists them)", action)
		}
	}
	textModes := l.modes & (inSearchBox | inBox)
	if a != nil {
		textModes &= a.modes()
	}
	for _, k := range splitKeys(keys) {
		spec, err := parseKeySpec(k)
		if err != nil {
			return err.Error()
		}
		for _, c := range spec.chords {
			if c.kind == chordByte && c.b == 0x03 {
				return "^C is the guaranteed way out; it cannot be rebound or unbound"
			}
			if textModes == 0 {
				continue
			}
			switch {
			case c.kind == chordByte && (c.b == 0x0d || c.b == 0x0a):
				return "Enter submits the " + textModeName(textModes) + " box; it cannot be taken from it"
			case c.kind == chordByte && c.b == 0x1b:
				return "Esc leaves the " + textModeName(textModes) + " box; it cannot be taken from it"
			case a != nil && c.kind == chordByte && c.b >= 0x20 && c.b < 0x7f:
				return fmt.Sprintf("%q is literal text in the %s box", string(c.b), textModeName(textModes))
			}
		}
	}
	return ""
}

func textModeName(m keyModeSet) string {
	if m&inBox != 0 {
		return "command"
	}
	return "search"
}

func defaultHelpOf(action string) helpID {
	for _, l := range defaultLayers {
		for _, b := range l.binds {
			if b.action == action && b.help != helpNone {
				return b.help
			}
		}
	}
	return helpNone
}

func touchedHelp(before, after []keyBinding) map[helpID]bool {
	sig := func(rows []keyBinding) map[helpID]string {
		parts := map[helpID][]string{}
		for _, r := range rows {
			if r.help != helpNone {
				parts[r.help] = append(parts[r.help], fmt.Sprintf("%v/%d/%s", r.chord, r.modes, r.action))
			}
		}
		out := map[helpID]string{}
		for h, p := range parts {
			sort.Strings(p)
			out[h] = strings.Join(p, " ")
		}
		return out
	}
	b, a := sig(before), sig(after)
	touched := map[helpID]bool{}
	for h := range b {
		if b[h] != a[h] {
			touched[h] = true
		}
	}
	for h := range a {
		if b[h] != a[h] {
			touched[h] = true
		}
	}
	return touched
}

func layerIndex(layers []keyLayer, name string) int {
	for i := range layers {
		if layers[i].name == name {
			return i
		}
	}
	return -1
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func renderLayers(layers []keyLayer) []string {
	var out []string
	for _, l := range layers {
		if len(l.binds) == 0 {
			continue
		}
		out = append(out, "", fmt.Sprintf("%s  (%s)", l.name, spellModes(l.modes)))
		w := 0
		for _, b := range l.binds {
			w = max(w, len(b.keys))
		}
		for _, b := range l.binds {
			action := b.action
			if action == "" {
				action = "(unbound)"
			}
			out = append(out, fmt.Sprintf("  %-*s  %s", w, b.keys, action))
		}
	}
	return out
}

func renderActions() []string {
	out := make([]string, 0, len(keyActions))
	w := 0
	for _, a := range keyActions {
		w = max(w, len(a.name))
	}
	for _, a := range keyActions {
		out = append(out, fmt.Sprintf("  %-*s  %s", w, a.name, spellModes(a.modes())))
	}
	return out
}

func helpDiagLine() string {
	if len(userKeyDiags) == 0 {
		return ""
	}
	return fmt.Sprintf("keys.toml: %s ignored, see `figaro doctor keys`", plural(len(userKeyDiags), "binding"))
}

func diagLines() []string {
	out := make([]string, 0, len(userKeyDiags))
	for _, d := range userKeyDiags {
		out = append(out, d.String())
	}
	return out
}
