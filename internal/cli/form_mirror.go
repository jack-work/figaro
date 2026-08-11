package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/rpc"
)

// formMirror is a client's OWN copy of an aria's form, kept live by applying the
// patches the server broadcasts.
//
// It is the same code the server runs: internal/form, the same persistent tree,
// the same semantic equality: so "the client's view" and "the aria's state" are
// not two implementations that have to be argued into agreement. A form is a
// state object; this is a second holder of one.
//
// RESYNC IS THE CONTRACT, not an error path. A delta whose version is not
// exactly one past ours means we missed something (a reconnect, a slow reader,
// an aria that moved while we were dialing), and the cure is to ask for the
// snapshot again: which is idempotent, because the version IS the durable
// index. Guessing, or applying out of order, is how a mirror silently diverges.
//
// A SCHEMA mismatch is a different failure wearing the same clothes, and it must
// not share an answer with a gap. A gap is transient and re-reading cures it; a
// peer speaking a shape we do not know will send the next delta in that same
// shape, so re-reading cures nothing and asking again per delta is a refetch
// storm against someone who will never agree. Say it once, stop applying.
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

// renderFormRow is one line: the indent, the marker a branch carries, the label,
// and a value clipped to what is left of the width.
func renderFormRow(n *formNode, width int, selected bool) string {
	marker := "  "
	if !n.leaf() {
		marker = "▸ "
	}
	indent := strings.Repeat("  ", n.depth-1)
	line := indent + marker + n.label
	if n.leaf() {
		line += ": " + formValuePreview(n.value)
	} else {
		line += fmt.Sprintf(" (%d)", len(n.children))
	}
	if len(line) > width {
		line = line[:max(0, width-1)] + "…"
	}
	if selected {
		return "\x1b[7m" + line + strings.Repeat(" ", max(0, width-len(line))) + "\x1b[0m"
	}
	return line
}

// formValuePreview keeps a value to one line. A form holds skill bodies measured
// in kilobytes; a tree that pastes one into a row is not a tree.
func formValuePreview(raw json.RawMessage) string {
	s := strings.Join(strings.Fields(string(raw)), " ")
	if len(s) > 120 {
		return s[:119] + "…"
	}
	return s
}

func openBranch(n *formNode, open map[string]bool) {
	if n.leaf() {
		return
	}
	open[n.path] = !open[n.path]
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
