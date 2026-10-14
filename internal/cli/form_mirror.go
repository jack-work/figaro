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

// formMirror is a client's own copy of an aria's form, kept live by the server's
// patches.
type formMirror struct {
	mu      sync.Mutex
	snap    form.Snapshot
	version uint64
	gaps    int
	// schema is non-zero once a peer sent an envelope version we cannot read:
	// the mirror has stopped tracking.
	schema int
}

type formApply int

const (
	formApplied formApply = iota
	// formResync: a delta was missed; re-read the snapshot.
	formResync
	// formIncompatible: the peer speaks a shape we do not.
	formIncompatible
)

func (m *formMirror) reset(snap form.Snapshot, version uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snap, m.version = snap, version
}

func (m *formMirror) apply(d rpc.FormDelta) formApply {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case d.Schema != rpc.FormDeltaSchema:
		m.schema = d.Schema
		return formIncompatible
	case d.Version <= m.version:
		return formApplied
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

// The expansion set is keyed by path, so a row keeps its state across a reorder.
type formNode struct {
	path     string
	label    string
	value    json.RawMessage
	children []*formNode
	depth    int
}

func (n *formNode) leaf() bool { return len(n.children) == 0 }

func buildFormTree(snap form.Snapshot) *formNode {
	root := &formNode{path: "", label: "form"}
	raw, err := snap.MarshalJSON()
	if err != nil {
		return root
	}
	addFormChildren(root, raw)
	sortFormTree(root)
	return root
}

func addFormChildren(parent *formNode, raw json.RawMessage) {
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil {
		return
	}
	for key, value := range members {
		path := key
		if parent.path != "" {
			path = parent.path + "." + key
		}
		n := &formNode{path: path, label: key, depth: parent.depth + 1}
		var inner map[string]json.RawMessage
		if json.Unmarshal(value, &inner) == nil && len(inner) > 0 {
			addFormChildren(n, value)
		} else {
			n.value = value
		}
		parent.children = append(parent.children, n)
	}
}

func sortFormTree(n *formNode) {
	sort.SliceStable(n.children, func(i, j int) bool { return n.children[i].label < n.children[j].label })
	for _, c := range n.children {
		sortFormTree(c)
	}
}

func flattenFormTree(n *formNode, open map[string]bool, out []*formNode) []*formNode {
	for _, c := range n.children {
		out = append(out, c)
		if !c.leaf() && open[c.path] {
			out = flattenFormTree(c, open, out)
		}
	}
	return out
}

func renderFormRow(n *formNode, width int, selected bool) string {
	indent := strings.Repeat("  ", n.depth-1)
	line := indent + n.label
	if n.leaf() {
		line += ": " + formValuePreview(n.value)
	} else {
		line += fmt.Sprintf(" (%d)", len(n.children))
	}
	// Columns, not bytes.
	line = clipToWidthEllipsis(line, width)
	if selected {
		return "\x1b[7m" + line + strings.Repeat(" ", max(0, width-displayWidth(line))) + "\x1b[0m"
	}
	return line
}

// A form holds skill bodies measured in kilobytes.
func formValuePreview(raw json.RawMessage) string {
	s := strings.Join(strings.Fields(string(raw)), " ")
	return clipToWidthEllipsis(s, 120)
}

func openBranch(n *formNode, open map[string]bool) {
	open[n.path] = !open[n.path]
}

// A JSON string that itself parses as JSON is unwrapped and indented.
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
	// Escapes are stripped at the source as well as at the paint; the yank is
	// untouched.
	wrapped := make([]string, 0, len(out))
	for _, l := range out {
		wrapped = append(wrapped, wrapPlain(pitText(strings.TrimRight(l, "\r")), max(width, 20))...)
	}
	return wrapped
}

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

// wrapPlain breaks at display width, one pass; wrappedValue caches it.
func wrapPlain(s string, width int) []string {
	if s == "" {
		return []string{""}
	}
	var out []string
	start, w, lastSpace := 0, 0, -1
	for i, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > width && i > start {
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

// A branch yanks as the object it stands for, so a yank is always valid JSON.
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

// OSC 52 works over ssh and in tmux without a helper binary; a terminal may refuse.
func copyToClipboard(w *os.File, text string) {
	fmt.Fprintf(w, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
