package cli

import (
	"fmt"
	"strings"
)

type keyImpl struct {
	modes keyModeSet
	pager pagerFunc
	input inputFunc
	opens bool
}

type keyAction struct {
	name  string
	impls []keyImpl
}

type keyBind struct {
	keys   string
	action string
	help   helpID
}

type keyLayer struct {
	name   string
	modes  keyModeSet
	prefix bool
	binds  []keyBind
}

const (
	prefixAction  = "prefix:"
	commandAction = ":"
)

const commandModes = inIncipit | inTranscript | inPanel | inVisual | inList | inComposer

func boundCommand(action string) (string, bool) {
	if !strings.HasPrefix(action, commandAction) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(action, commandAction)), true
}

func commandBinding(action string) *keyAction {
	line, ok := boundCommand(action)
	if !ok || line == "" {
		return nil
	}
	a := named(action, atInput(commandModes, func(in *interactiveInput, _ keyEvent) keyVerdict {
		in.mu.Lock()
		defer in.mu.Unlock()
		if in.lt.tr.command == nil {
			in.lt.tr.setCommandNoteAt(line+": this session cannot run commands", alertError)
			return keyHandled
		}
		in.lt.tr.command(line)
		return keyHandled
	}))
	return &a
}

func atPager(m keyModeSet, f pagerFunc) keyImpl      { return keyImpl{modes: m, pager: f} }
func atPagerOpens(m keyModeSet, f pagerFunc) keyImpl { return keyImpl{modes: m, pager: f, opens: true} }
func atInput(m keyModeSet, f inputFunc) keyImpl      { return keyImpl{modes: m, input: f} }
func atInputOpens(m keyModeSet, f inputFunc) keyImpl { return keyImpl{modes: m, input: f, opens: true} }

func named(name string, impls ...keyImpl) keyAction { return keyAction{name: name, impls: impls} }

func bind(keys, action string, help helpID) keyBind {
	return keyBind{keys: keys, action: action, help: help}
}

func (a *keyAction) implIn(m keyMode) *keyImpl {
	for i := range a.impls {
		if a.impls[i].modes&(1<<m) != 0 {
			return &a.impls[i]
		}
	}
	return nil
}

func (a *keyAction) modes() keyModeSet {
	var all keyModeSet
	for _, im := range a.impls {
		all |= im.modes
	}
	return all
}

var actionByName = func() map[string]*keyAction {
	m := make(map[string]*keyAction, len(keyActions))
	for i := range keyActions {
		a := &keyActions[i]
		if m[a.name] != nil {
			panic("keymap: two actions named " + a.name)
		}
		m[a.name] = a
	}
	return m
}()

func layerByName(layers []keyLayer, name string) *keyLayer {
	for i := range layers {
		if layers[i].name == name {
			return &layers[i]
		}
	}
	return nil
}

func splitKeys(keys string) []string { return strings.Fields(keys) }

type compileDiag struct {
	layer string
	keys  string
	text  string
}

func (d compileDiag) String() string {
	return fmt.Sprintf("[%s] %s: %s", d.layer, d.keys, d.text)
}

func prefixArming(layers []keyLayer) map[string]*keyAction {
	out := map[string]*keyAction{}
	for _, l := range layers {
		if !l.prefix {
			continue
		}
		name := l.name
		a := named(prefixAction+name, atPagerOpens(l.modes, func(t *transcript) { t.pending = name }))
		out[a.name] = &a
	}
	return out
}

func compileLayers(layers []keyLayer) ([]keyBinding, []compileDiag) {
	var rows []keyBinding
	var diags []compileDiag
	arming := prefixArming(layers)
	claims := map[string]map[chord]keyModeSet{}
	for _, l := range layers {
		scope := ""
		if l.prefix {
			scope = l.name
		}
		if claims[scope] == nil {
			claims[scope] = map[chord]keyModeSet{}
		}
		claimed := claims[scope]
		for _, b := range l.binds {
			if b.action == "" {
				for _, k := range splitKeys(b.keys) {
					spec, err := parseKeySpec(k)
					if err != nil {
						diags = append(diags, compileDiag{l.name, k, err.Error()})
						continue
					}
					for _, c := range spec.chords {
						claimed[c] |= l.modes
					}
				}
				continue
			}
			a := actionByName[b.action]
			if a == nil {
				a = arming[b.action]
			}
			if a == nil {
				a = commandBinding(b.action)
			}
			if a == nil {
				diags = append(diags, compileDiag{l.name, b.keys, fmt.Sprintf("no action named %q", b.action)})
				continue
			}
			if l.prefix && strings.HasPrefix(a.name, prefixAction) {
				diags = append(diags, compileDiag{l.name, b.keys, "a prefix cannot arm another prefix"})
				continue
			}
			if a.modes()&l.modes == 0 {
				diags = append(diags, compileDiag{l.name, b.keys, fmt.Sprintf("%s does nothing in any mode this layer applies to", b.action)})
				continue
			}
			for _, k := range splitKeys(b.keys) {
				spec, err := parseKeySpec(k)
				if err != nil {
					diags = append(diags, compileDiag{l.name, k, err.Error()})
					continue
				}
				for _, c := range spec.chords {
					for _, im := range a.impls {
						m := im.modes & l.modes &^ claimed[c]
						if im.pager != nil {
							m &^= inIncipit
						}
						if m == 0 {
							continue
						}
						claimed[c] |= m
						open := staysInline
						if im.opens {
							open = opensPager
						}
						if l.prefix {
							open = staysInline
						}
						rows = append(rows, keyBinding{
							chord: c, modes: m, open: open, help: b.help,
							pager: im.pager, input: im.input, action: a.name, layer: l.name, prefix: scope,
						})
					}
				}
			}
		}
	}
	return rows, diags
}

func mustCompile(layers []keyLayer) []keyBinding {
	rows, diags := compileLayers(layers)
	if len(diags) > 0 {
		panic("keymap: the default layers do not compile: " + diags[0].String())
	}
	return rows
}
