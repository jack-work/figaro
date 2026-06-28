package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/rpc"
)

// livelogTurn renders a turn through the inline-seal renderer (internal/livelog),
// opt-in via FIGARO_LIVELOG. Closed messages are sealed to native scrollback once
// and never redrawn; only the open message is a live region, so a terminal resize
// repaints just that bounded part — the structural fix for the resize/dup class.
//
// It translates figaro's positional wire ops into aria blocks (id "n<index>",
// monotonic version) and reuses figaro's own node renderers via ariaView, so the
// on-screen content is identical to the default painter. Phase 1 carries each
// block's full text; deltas (Phase 2) are pure compression of the same item.
type livelogTurn struct {
	in   *ldrender.Inline
	term *ldrender.ANSITerminal
	view *ariaView

	nextLT int
	openLT int
	order  []string // open-message block ids, in order
	node   map[string]aria.Node
}

func newLivelogTurn(out io.Writer, w, h int, settings *renderSettings, bookend func() string) *livelogTurn {
	term := ldrender.NewANSITerminal(out, w, h)
	view := &ariaView{settings: settings}
	in := ldrender.NewInline(term, view)
	in.Bookend = bookend
	return &livelogTurn{in: in, term: term, view: view, node: map[string]aria.Node{}}
}

func (t *livelogTurn) handle(method string, params json.RawMessage) {
	switch method {
	case rpc.MethodLogSnapshot:
		var e rpc.SnapshotEntry
		if json.Unmarshal(params, &e) != nil {
			return
		}
		if e.Role == "assistant" {
			t.openLT = t.nextSeq()
			t.order, t.node = nil, map[string]aria.Node{}
			for i, n := range e.Nodes {
				t.setBlock(i, n)
			}
			t.repaint()
		} else {
			// a closed (e.g. user) message — seal it once.
			lt := t.nextSeq()
			var nodes []aria.Node
			for i, n := range e.Nodes {
				nodes = append(nodes, toAria(fmt.Sprintf("u%d", i), 1, n))
			}
			t.in.Seal(aria.Message{LT: lt, Role: e.Role, Nodes: nodes})
		}
	case rpc.MethodNodeOpen:
		var e rpc.NodeOpenEntry
		if json.Unmarshal(params, &e) == nil {
			t.setBlock(e.Index, e.Node)
			t.repaint()
		}
	case rpc.MethodNodePatch:
		var e rpc.NodePatchEntry
		if json.Unmarshal(params, &e) == nil {
			t.patchBlock(e.Index, e.Field, e.At, e.Del, e.Ins)
			t.repaint()
		}
	case rpc.MethodNodeSet:
		var e rpc.NodeSetEntry
		if json.Unmarshal(params, &e) == nil {
			t.setScalars(e.Index, e.Status, e.Name, e.Args)
			t.repaint()
		}
	case rpc.MethodLogCommit:
		t.in.Seal(aria.Message{LT: t.openLT, Role: "assistant", Nodes: t.openNodes()})
		t.openLT, t.order, t.node = 0, nil, map[string]aria.Node{}
	}
}

func (t *livelogTurn) tick()           { t.in.Tick(t.openNodes()) }
func (t *livelogTurn) resize(w, h int) { t.term.SetSize(w, h); t.in.Resize(t.openNodes()) }
func (t *livelogTurn) render()         { t.repaint() } // re-render open (e.g. after a verbosity toggle)

func (t *livelogTurn) repaint() {
	if t.openLT != 0 {
		t.in.Open(t.openLT, "assistant", t.openNodes())
	}
}

func (t *livelogTurn) nextSeq() int { t.nextLT++; return t.nextLT }

func (t *livelogTurn) id(index int) string { return fmt.Sprintf("n%d", index) }

func (t *livelogTurn) setBlock(index int, n livedoc.Node) {
	id := t.id(index)
	cur, ok := t.node[id]
	if !ok {
		t.order = append(t.order, id)
	}
	t.node[id] = toAria(id, cur.Version+1, n)
}

func (t *livelogTurn) patchBlock(index int, field string, at, del int, ins string) {
	id := t.id(index)
	cur, ok := t.node[id]
	if !ok {
		t.order = append(t.order, id)
		cur.ID = id
	}
	if field == "output" {
		cur.Output = splice(cur.Output, at, del, ins)
	} else {
		cur.Markdown = splice(cur.Markdown, at, del, ins)
	}
	cur.Version++
	t.node[id] = cur
}

func (t *livelogTurn) setScalars(index int, status, name string, args map[string]interface{}) {
	id := t.id(index)
	cur, ok := t.node[id]
	if !ok {
		t.order = append(t.order, id)
		cur.ID = id
	}
	if status != "" {
		cur.Status = status
	}
	if name != "" {
		cur.Name = name
	}
	if args != nil {
		cur.Args = args
	}
	cur.Version++
	t.node[id] = cur
}

func (t *livelogTurn) openNodes() []aria.Node {
	out := make([]aria.Node, 0, len(t.order))
	for _, id := range t.order {
		out = append(out, t.node[id])
	}
	return out
}

// toAria converts a figaro wire node into an aria block at the given version.
func toAria(id string, version int, n livedoc.Node) aria.Node {
	return aria.Node{
		ID: id, Version: version,
		Type: string(n.Type), Markdown: n.Markdown,
		Name: n.Name, Args: n.Args, Status: n.Status, Output: n.Output,
	}
}

// splice applies a clamped single-region edit (the wire's delta primitive).
func splice(s string, at, del int, ins string) string {
	if at < 0 {
		at = 0
	}
	if at > len(s) {
		at = len(s)
	}
	end := at + del
	if end < at {
		end = at
	}
	if end > len(s) {
		end = len(s)
	}
	return s[:at] + ins + s[end:]
}

// ariaView renders an aria block by reconstructing a figaro node and reusing
// figaro's existing renderers (glamour prose, tool widgets), so content matches
// the default painter exactly.
type ariaView struct{ settings *renderSettings }

func (v *ariaView) Render(n aria.Node, width, tick int) []string {
	ln := livedoc.Node{Type: typeOf(n.Type), Name: n.Name, Status: n.Status, Args: n.Args}
	switch ln.Type {
	case livedoc.NodeTool:
		ln.Output = n.Output
		return renderToolNode(ln, width, nodeBashCapDefault, uint64(tick), v.settings != nil && v.settings.verbose)
	case livedoc.NodeThinking:
		ln.Markdown = n.Markdown
		return renderThinkingNode(ln, width)
	default:
		ln.Markdown = n.Markdown
		return renderProseNode(ln, width)
	}
}

func typeOf(k string) livedoc.NodeType {
	switch k {
	case "tool":
		return livedoc.NodeTool
	case "thinking":
		return livedoc.NodeThinking
	default:
		return livedoc.NodeProse
	}
}
