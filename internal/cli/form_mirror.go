package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/mattn/go-runewidth"
)

// formMirror is a client's own copy of an aria's form, kept live by the server's
// patches.
type formMirror struct {
	mu          sync.Mutex
	snap        form.Snapshot
	incarnation uint64
	version     uint64
	gaps        int
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

func (m *formMirror) reset(snap form.Snapshot, incarnation, version uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if incarnation < m.incarnation || incarnation == m.incarnation && version < m.version {
		return
	}
	m.snap, m.incarnation, m.version = snap, incarnation, version
}

func (m *formMirror) apply(d rpc.FormDelta) formApply {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case d.Schema != rpc.FormDeltaSchema:
		m.schema = d.Schema
		return formIncompatible
	case d.Incarnation < m.incarnation:
		return formApplied
	case d.Incarnation > m.incarnation:
		m.gaps++
		return formResync
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

func (m *formMirror) Shown() form.Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snap
}

func (m *formMirror) Version() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.version
}

// The expansion set is keyed by path, so a row keeps its state across a reorder.
type formNode struct {
	path     string
	label    string
	value    json.RawMessage
	children []*formNode
	depth    int
	branch   bool
	count    int

	node      form.Node
	stamp     form.Stamp
	kidsStamp form.Stamp
	kidsBuilt bool
	row       string
	rowWidth  int
	width     int

	guided   bool
	guide    string
	cont     string
	pad      int
	open     bool
	rowGuide string
	rowPad   int
	rowOpen  bool
	tail     int
}

func (n *formNode) leaf() bool { return !n.branch && len(n.children) == 0 }

func (n *formNode) size() int {
	if n.branch {
		return n.count
	}
	return len(n.children)
}

func (n *formNode) take(fn form.Node) {
	st := fn.Stamp()
	if n.node.Valid() && n.stamp == st {
		n.node = fn
		return
	}
	n.node, n.stamp = fn, st
	n.branch = fn.Branch()
	n.rowWidth = 0
	if n.branch {
		n.count, n.value = fn.Len(), nil
		return
	}
	n.count, n.children, n.kidsBuilt = 0, nil, false
	n.value = fn.Value().Raw()
}

func (n *formNode) kids() []*formNode {
	if !n.branch {
		return n.children
	}
	if n.kidsBuilt && n.kidsStamp == n.stamp {
		return n.children
	}
	old := n.children
	next := make([]*formNode, 0, n.count)
	i := 0
	for c := range n.node.Children() {
		key := c.Key()
		for i < len(old) && old[i].label < key {
			i++
		}
		var vn *formNode
		if i < len(old) && old[i].label == key {
			vn = old[i]
			i++
		} else {
			path := key
			if n.path != "" {
				path = n.path + "." + key
			}
			vn = &formNode{path: path, label: key, depth: n.depth + 1}
		}
		vn.take(c)
		next = append(next, vn)
	}
	n.children, n.kidsStamp, n.kidsBuilt = next, n.stamp, true
	return next
}

func (n *formNode) rendered(width int) string {
	if n.rowWidth != width || n.rowGuide != n.guide || n.rowPad != n.pad || n.rowOpen != n.open {
		n.row, n.tail = renderTreeRow(n, width)
		n.rowWidth, n.rowGuide, n.rowPad, n.rowOpen = width, n.guide, n.pad, n.open
	}
	return n.row
}

func (n *formNode) rowDims() (lead, tail int) { return len(n.lead()), n.tail }

func (n *formNode) lead() string {
	if n.guided {
		return n.guide
	}
	return strings.Repeat("  ", max(n.depth-1, 0))
}

func (n *formNode) yankText() string { return yankFormNode(n) }

func (n *formNode) rowText() string { return n.rendered(n.width) }

func newFormRoot(snap form.Snapshot) *formNode {
	root := &formNode{path: "", label: "form"}
	root.take(snap.RootNode())
	root.branch = true
	return root
}

func flattenGuided(n *formNode, open map[string]bool, out []*formNode, cont string, top bool) []*formNode {
	w := guideWalk{open: open, out: out}
	w.walk(n, cont, top, nil, nil)
	return w.out
}

type guideWalk struct {
	open  map[string]bool
	out   []*formNode
	shape *viewShape
}

func (w *guideWalk) walk(n *formNode, cont string, top bool, labels []string, row *viewRowKey) {
	kids := n.kids()
	if w.shape != nil {
		kids = w.shape.order(labels, kids, w.open)
	}
	pad := 0
	for _, c := range kids {
		if c.leaf() {
			pad = max(pad, min(displayWidth(c.label), labelPadMax))
		}
	}
	for i, c := range kids {
		last := i == len(kids)-1
		c.guided, c.pad = true, pad
		c.open = !c.leaf() && w.open[c.path]
		switch {
		case top:
			c.guide, c.cont = "", ""
		case last:
			c.guide, c.cont = cont+"└─ ", cont+"   "
		default:
			c.guide, c.cont = cont+"├─ ", cont+"│  "
		}
		if c.leaf() && c.label != "" && displayWidth(c.label) > labelPadMax {
			c.pad = 0
		}
		w.out = append(w.out, c)
		segs := append(labels[:len(labels):len(labels)], c.label)
		under := row
		if w.shape != nil {
			under = w.shape.note(c, segs, row)
		}
		if c.open {
			w.walk(c, c.cont, false, segs, under)
		}
	}
}

const labelPadMax = 24

func flattenFormTree(n *formNode, open map[string]bool, out []*formNode) []*formNode {
	for _, c := range n.kids() {
		out = append(out, c)
		if !c.leaf() && open[c.path] {
			out = flattenFormTree(c, open, out)
		}
	}
	return out
}

func renderTreeRow(n *formNode, width int) (string, int) {
	line := n.lead() + n.label
	tail := 0
	switch {
	case n.leaf():
		line += ": " + strings.Repeat(" ", max(n.pad-displayWidth(n.label), 0)) + formValuePreview(n.value)
	case n.open:
		line += fmt.Sprintf(" (%d)", n.size())
	default:
		if inline, ok := inlineBranch(n); ok {
			line += ": " + inline
			break
		}
		line += fmt.Sprintf(" (%d)", n.size())
		if peek := branchPeek(n); peek != "" {
			line += "  "
			tail = len(line)
			line += peek
		}
	}
	line = clipToWidthEllipsis(line, width)
	if tail >= len(line) {
		tail = 0
	}
	return line, tail
}

const inlineMax = 3

func inlineBranch(n *formNode) (string, bool) {
	if !n.node.Valid() || n.size() == 0 || n.size() > inlineMax {
		return "", false
	}
	var parts []string
	for c := range n.node.Children() {
		if !c.Leaf() {
			return "", false
		}
		raw := c.Value().Raw()
		if t := bytes.TrimSpace(raw); len(t) > 0 && (t[0] == '{' || t[0] == '[') {
			return "", false
		}
		parts = append(parts, c.Key()+": "+formValuePreview(raw))
	}
	text := "{" + strings.Join(parts, ", ") + "}"
	if displayWidth(text) > 60 {
		return "", false
	}
	return text, true
}

const peekKeys = 4

func branchPeek(n *formNode) string {
	if !n.node.Valid() {
		return ""
	}
	var names []string
	more := false
	for c := range n.node.Children() {
		if len(names) == peekKeys {
			more = true
			break
		}
		names = append(names, c.Key())
	}
	if len(names) == 0 {
		return ""
	}
	text := strings.Join(names, " · ")
	if more {
		text += " …"
	}
	return text
}

func renderFormRow(n *formNode, width int, selected bool) string {
	indent := n.lead()
	line := indent + n.label
	if n.leaf() {
		line += ": " + formValuePreview(n.value)
	} else {
		line += fmt.Sprintf(" (%d)", n.size())
	}
	// Columns, not bytes.
	line = clipToWidthEllipsis(line, width)
	if selected {
		return "\x1b[7m" + line + strings.Repeat(" ", max(0, width-displayWidth(line))) + "\x1b[0m"
	}
	return line
}

func formValuePreview(raw json.RawMessage) string {
	const cols = 120
	var b strings.Builder
	w, space := 0, false
	for _, r := range string(raw) {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			w++
			space = false
		}
		b.WriteRune(r)
		w += runewidth.RuneWidth(r)
		if w > cols+1 {
			break
		}
	}
	return clipToWidthEllipsis(b.String(), cols)
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
	var raw json.RawMessage
	if n.node.Valid() {
		enc, err := n.node.Encode()
		if err != nil {
			return ""
		}
		raw = enc
	} else {
		raw = encodeFormChildren(n)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return ""
	}
	return buf.String()
}

func encodeFormChildren(n *formNode) json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, c := range n.children {
		if c.leaf() {
			out[c.label] = c.value
			continue
		}
		out[c.label] = encodeFormChildren(c)
	}
	b, _ := json.Marshal(out)
	return b
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
