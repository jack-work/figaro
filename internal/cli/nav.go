package cli

import (
	"strconv"
	"strings"
)

func (t *transcript) noteStep(msg string) {
	if msg != "" {
		t.note(msg)
	}
}

var navKinds = map[string]string{
	"": "", "node": "", "nodes": "",
	"delta": "delta", "deltas": "delta",
	"fork": "fork", "forks": "fork",
	"turn": "turn", "turns": "turn",
	"block": "block", "blocks": "block",
}

func parseNav(text string) (kind string, dir int, ok bool, err string) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", 0, false, ""
	}
	switch fields[0] {
	case "next":
		dir = 1
	case "prev", "previous":
		dir = -1
	default:
		return "", 0, false, ""
	}
	if len(fields) > 2 {
		return "", 0, true, fields[0] + ": one kind at most (delta, fork, turn, block)"
	}
	word := ""
	if len(fields) == 2 {
		word = fields[1]
	}
	kind, known := navKinds[word]
	if !known {
		return "", 0, true, fields[0] + ": " + strconv.Quote(word) + " is not a kind (delta, fork, turn, block)"
	}
	return kind, dir, true, ""
}

func (t *transcript) navigate(kind string, dir int) string {
	switch kind {
	case "":
		t.selectNode(dir, false)
		return ""
	case "block":
		return t.stepBlock(dir)
	case "delta":
		return t.stepDelta(dir)
	case "fork":
		t.jumpFork(dir)
		return ""
	case "turn":
		t.jumpTurn(dir)
		return ""
	}
	return "nowhere to go"
}

func (t *transcript) navStart(refs []selectionPoint, dir int) int {
	if t.selection.active {
		for i, p := range refs {
			if p.nodeRef == t.selection.focus.nodeRef {
				return i
			}
		}
	}
	t.stopFollowing()
	t.buildIndex()
	if ref, ok := t.viewportSeedRef(dir); ok {
		for i, p := range refs {
			if p.nodeRef == ref {
				return i - dir
			}
		}
	}
	if dir > 0 {
		return -1
	}
	return len(refs)
}

func (t *transcript) stepTo(want func(selectionPoint) bool, dir int) (selectionPoint, bool) {
	t.settle()
	refs := t.nodeRefs()
	for i := t.navStart(refs, dir) + dir; i >= 0 && i < len(refs); i += dir {
		if want(refs[i]) {
			return refs[i], true
		}
	}
	return selectionPoint{}, false
}

func (t *transcript) land(ref nodeRef) {
	t.wantTop = false
	t.stopFollowing()
	if t.selectRef(ref, false) {
		t.ensureSelectionVisible()
	}
}

func (t *transcript) stepBlock(dir int) string {
	p, ok := t.stepTo(func(p selectionPoint) bool { return !p.pseudo() }, dir)
	if !ok {
		return "no block " + towards(dir)
	}
	t.land(p.nodeRef)
	return ""
}

func (t *transcript) stepDelta(dir int) string {
	p, ok := t.stepTo(func(p selectionPoint) bool {
		if p.delta > 0 {
			return true
		}
		if p.pseudo() || t.adorned[p.nodeRef] {
			return false
		}
		d, has := t.deltasAt(p.nodeRef)
		return has && adornRowCount(d, adornLift(p.index)) > 0
	}, dir)
	if !ok {
		return "no form delta " + towards(dir)
	}
	if p.delta > 0 {
		t.land(p.nodeRef)
		return ""
	}
	t.toggleFolds([]foldTarget{{state: t.adorned, ref: p.nodeRef, deltas: true}})
	d, _ := t.deltasAt(p.nodeRef)
	row := 1
	if dir < 0 {
		row = adornRowCount(d, adornLift(p.index))
	}
	t.land(deltaRefOf(p.nodeRef, row))
	return ""
}

func towards(dir int) string {
	if dir > 0 {
		return "below"
	}
	return "above"
}
