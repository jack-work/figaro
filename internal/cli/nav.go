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
	"comment": "comment", "comments": "comment",
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
		return "", 0, true, fields[0] + ": one kind at most (comment, delta, fork, turn, block)"
	}
	word := ""
	if len(fields) == 2 {
		word = fields[1]
	}
	kind, known := navKinds[word]
	if !known {
		return "", 0, true, fields[0] + ": " + strconv.Quote(word) + " is not a kind (comment, delta, fork, turn, block)"
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
	case "comment":
		return t.stepComment(dir)
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
	return t.stepEntry(dir, func(e metaEntry) bool { return e.kind == metaDelta }, "no form delta")
}

func (t *transcript) stepEntry(dir int, want func(metaEntry) bool, none string) string {
	p, ok := t.stepTo(func(p selectionPoint) bool {
		if p.reply > 0 {
			return false
		}
		if p.meta > 0 {
			e, ok := t.entryAt(p.nodeRef)
			return ok && want(e)
		}
		return !t.metaOpen[p.nodeRef] && t.firstEntry(p.nodeRef, want, dir) > 0
	}, dir)
	if !ok {
		return none + " " + towards(dir)
	}
	if p.meta > 0 {
		t.land(p.nodeRef)
		return ""
	}
	i := t.firstEntry(p.nodeRef, want, dir)
	t.toggleFolds([]foldTarget{{state: t.metaOpen, ref: p.nodeRef, deltas: true}})
	t.land(metaRefOf(p.nodeRef, i))
	return ""
}

func (t *transcript) firstEntry(ref nodeRef, want func(metaEntry) bool, dir int) int {
	entries := t.entriesAt(ref)
	found := 0
	for i, e := range entries {
		if !want(e) {
			continue
		}
		found = i + 1
		if dir > 0 {
			break
		}
	}
	return found
}

func towards(dir int) string {
	if dir > 0 {
		return "below"
	}
	return "above"
}
