package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/mattn/go-runewidth"
)

// formMirror is a client's OWN copy of an aria's form, kept live by applying the
// patches the server broadcasts.
type formMirror struct {
	mu      sync.Mutex
	snap    form.Snapshot
	version uint64
	// gaps counts resyncs, so a listener can say it noticed instead of hiding it.
	gaps int
	// schema is the envelope version a peer sent that we could not read, once
	// seen. Non-zero means this mirror has stopped tracking.
	schema int
}

// formApply is what a delta did. Three outcomes, because there are three
// different things to do about them.
type formApply int

const (
	// formApplied: folded in, or already held.
	formApplied formApply = iota
	// formResync: we missed one. Re-read the snapshot.
	formResync
	// formIncompatible: the peer speaks a shape we do not. Nothing to re-read.
	formIncompatible
)

func (m *formMirror) reset(snap form.Snapshot, version uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snap, m.version = snap, version
}

// apply folds a delta in. Returns false when the delta does not follow ours, in
// which case the caller must re-read the snapshot.
func (m *formMirror) apply(d rpc.FormDelta) formApply {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case d.Schema != rpc.FormDeltaSchema:
		m.schema = d.Schema
		return formIncompatible
	case d.Version <= m.version:
		return formApplied // already have it; a replay after resync is not a gap
	case d.Version != m.version+1:
		m.gaps++
		return formResync
	}
	m.snap = m.snap.Apply(d.Patch)
	m.version = d.Version
	return formApplied
}

func (m *formMirror) state() (form.Snapshot, uint64, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snap, m.version, m.gaps
}

// formNode is one row of the rendered tree. The tree is rebuilt from the mirror
// on every change and the expansion set is keyed by PATH, so a row keeps its
// open/closed state across an update that reorders or replaces its neighbours.
type formNode struct {
	path     string
	label    string
	value    json.RawMessage // leaf only
	children []*formNode
	depth    int
}

func (n *formNode) leaf() bool { return len(n.children) == 0 }

// buildFormTree turns the flat dotted keys into the tree they describe. Same
// rule as `fig state`: a key whose prefix is already a leaf keeps its dotted
// name rather than making that leaf unreachable.
func buildFormTree(snap form.Snapshot) *formNode {
	root := &formNode{path: "", label: "form"}
	index := map[string]*formNode{"": root}
	for key, raw := range snap.All() {
		segments := strings.Split(key, ".")
		parent, prefix, ok := root, "", true
		for _, seg := range segments[:len(segments)-1] {
			if prefix == "" {
				prefix = seg
			} else {
				prefix += "." + seg
			}
			child, seen := index[prefix]
			if seen && child.leaf() && child.value != nil {
				ok = false
				break
			}
			if !seen {
				child = &formNode{path: prefix, label: seg, depth: parent.depth + 1}
				index[prefix] = child
				parent.children = append(parent.children, child)
			}
			parent = child
		}
		if !ok {
			root.children = append(root.children,
				&formNode{path: key, label: key, value: raw, depth: 1})
			continue
		}
		leafLabel := segments[len(segments)-1]
		if existing, seen := index[key]; seen && !existing.leaf() {
			root.children = append(root.children,
				&formNode{path: key, label: key, value: raw, depth: 1})
			continue
		}
		node := &formNode{path: key, label: leafLabel, value: raw, depth: parent.depth + 1}
		index[key] = node
		parent.children = append(parent.children, node)
	}
	sortFormTree(root)
	return root
}

func sortFormTree(n *formNode) {
	sort.SliceStable(n.children, func(i, j int) bool { return n.children[i].label < n.children[j].label })
	for _, c := range n.children {
		sortFormTree(c)
	}
}

// flattenFormTree is the visible rows, in order, honouring the expansion set.
func flattenFormTree(n *formNode, open map[string]bool, out []*formNode) []*formNode {
	for _, c := range n.children {
		out = append(out, c)
		if !c.leaf() && open[c.path] {
			out = flattenFormTree(c, open, out)
		}
	}
	return out
}

// renderFormRow is one line: the indent, the label, and a value clipped to what
// is left of the width.
//
// NO MARKER ON A BRANCH. Every branch carried a "▸" and every leaf carried two
// spaces to line up with it -- a column spent saying what the indent of the
// rows underneath already says, and what the "(8)" at the end of a branch says
// again. Gluck: "the arrow that prefaces top level keys that have nested
// children can be dropped. Indentation covers it enough."
func renderFormRow(n *formNode, width int, selected bool) string {
	indent := strings.Repeat("  ", n.depth-1)
	line := indent + n.label
	if n.leaf() {
		line += ": " + formValuePreview(n.value)
	} else {
		line += fmt.Sprintf(" (%d)", len(n.children))
	}
	// COLUMNS, NOT BYTES. Both cuts in this file used len(): a row of Japanese
	// painted 75 columns of a 100-column pane and then ellipsised, and the
	// preview below cut mid-rune and left two U+FFFD in the middle of the
	// line. An emoji spends four of a byte budget and two cells of a real one.
	line = clipToWidthEllipsis(line, width)
	if selected {
		return "\x1b[7m" + line + strings.Repeat(" ", max(0, width-displayWidth(line))) + "\x1b[0m"
	}
	return line
}

// formValuePreview keeps a value to one line. A form holds skill bodies measured
// in kilobytes; a tree that pastes one into a row is not a tree.
func formValuePreview(raw json.RawMessage) string {
	s := strings.Join(strings.Fields(string(raw)), " ")
	// 120 COLUMNS, not 120 bytes: a byte cap gave an ASCII value 120
	// characters of preview and a CJK value 40, and cut the 121st rune in
	// half on the way.
	return clipToWidthEllipsis(s, 120)
}

// openBranch is Enter: it opens what is under a row. On a BRANCH that is its
// children; on a LEAF it is the value itself, spelled out over as many rows as
// it takes -- because a form holds skill frontmatter and file contents, and a
// value clipped to one row with an ellipsis is a value you cannot read.
func openBranch(n *formNode, open map[string]bool) {
	open[n.path] = !open[n.path]
}

// formValueLines is an opened leaf: the value, made readable.
//
// THREE SHAPES, and the first two are the ones a form actually holds. A JSON
// object or array is INDENTED. A JSON string that itself parses as an object
// or an array -- which is how a skill's frontmatter, a tool result or a
// serialised file arrives -- is unwrapped and then indented, because the outer
// quotes are the least interesting thing about it. Anything else is the plain
// string, its own newlines honoured.
func formValueLines(raw json.RawMessage, width int) []string {
	var out []string
	if lines, ok := prettyJSON(raw); ok {
		out = lines
	} else {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			out = strings.Split(string(raw), "\n")
		} else if lines, ok := prettyJSON(json.RawMessage(s)); ok {
			out = lines
		} else {
			out = strings.Split(s, "\n")
		}
	}
	// Wrap rather than clip: the point of opening a value is to read all of it.
	wrapped := make([]string, 0, len(out))
	for _, l := range out {
		wrapped = append(wrapped, wrapPlain(strings.TrimRight(l, "\r"), max(width, 20))...)
	}
	return wrapped
}

// prettyJSON indents b when b is a JSON object or array, and refuses otherwise:
// a bare number or a quoted string gains nothing from indentation and would
// come back with its quotes still on.
func prettyJSON(b []byte) ([]string, bool) {
	t := bytes.TrimSpace(b)
	if len(t) == 0 || (t[0] != '{' && t[0] != '[') {
		return nil, false
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, t, "", "  "); err != nil {
		return nil, false
	}
	return strings.Split(buf.String(), "\n"), true
}

// wrapPlain breaks a line at the width, at a space where there is one within
// reach and mid-word where there is not. Display width, not bytes: a form is
// full of prose and emoji.
// ONE PASS, NOT ONE PER ROW. The first cut measured the whole remaining string
// with runewidth.StringWidth on every iteration, which is O(n²) in the length
// of the value -- and Items() re-wrapped every open value on every paint, under
// the render lock. Measured by a reviewer: 120KB of value cost 117ms PER
// KEYSTROKE, and a 1MB value 3.53 SECONDS per render. This walks the string
// once; wrappedValue caches the result so a repaint costs nothing at all.
func wrapPlain(s string, width int) []string {
	if s == "" {
		return []string{""}
	}
	var out []string
	start, w, lastSpace := 0, 0, -1
	for i, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > width && i > start {
			// Break at a space when there is one past the halfway mark;
			// wrapping a word's first letter onto its own row helps nobody.
			if lastSpace > start && lastSpace-start > width/2 {
				out = append(out, s[start:lastSpace])
				start = lastSpace + 1
			} else {
				out = append(out, s[start:i])
				start = i
			}
			lastSpace, w = -1, 0
			for _, rr := range s[start:i] {
				w += runewidth.RuneWidth(rr)
			}
		}
		if r == ' ' {
			lastSpace = i
		}
		w += rw
	}
	return append(out, s[start:])
}

// yankFormNode is what `y` copies: a leaf's value, or a branch as the object it
// stands for, so a yank is always valid JSON.
func yankFormNode(n *formNode) string {
	if n.leaf() {
		return string(n.value)
	}
	out := map[string]any{}
	var walk func(*formNode, map[string]any)
	walk = func(node *formNode, into map[string]any) {
		for _, c := range node.children {
			if c.leaf() {
				into[c.label] = json.RawMessage(c.value)
				continue
			}
			sub := map[string]any{}
			into[c.label] = sub
			walk(c, sub)
		}
	}
	walk(n, out)
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

// copyToClipboard writes through OSC 52, so it works over ssh and in tmux
// without a helper binary. Best effort by nature: a terminal may refuse.
func copyToClipboard(w *os.File, text string) {
	fmt.Fprintf(w, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
