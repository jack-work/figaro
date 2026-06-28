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
// opt-in via FIGARO_LIVELOG. Closed messages seal to native scrollback once and
// are never redrawn; only the open message is a live region, so a terminal resize
// repaints just that bounded part — the structural fix for the resize/dup class.
//
// Until the wire itself is aria (next phase), this folds the current positional
// op-zoo into the one canonical representation — livedoc.Node, the same block the
// renderer draws — assigning a stable id ("n<index>") and a version. There is no
// second node type: the bridge builds livedoc.Node directly.
type livelogTurn struct {
	in   *ldrender.Inline
	term *ldrender.ANSITerminal

	nextLT int
	openLT int
	order  []string // open-message block ids, in order
	node   map[string]livedoc.Node
}

func newLivelogTurn(out io.Writer, w, h int, settings *renderSettings, bookend func() string) *livelogTurn {
	term := ldrender.NewANSITerminal(out, w, h)
	in := ldrender.NewInline(term, &ariaView{settings: settings})
	in.Bookend = bookend
	return &livelogTurn{in: in, term: term, node: map[string]livedoc.Node{}}
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
			t.order, t.node = nil, map[string]livedoc.Node{}
			for i, n := range e.Nodes {
				t.setBlock(i, n)
			}
			t.repaint()
		} else {
			// a closed (e.g. user) message — seal it once.
			lt := t.nextSeq()
			nodes := make([]livedoc.Node, len(e.Nodes))
			for i, n := range e.Nodes {
				n.ID, n.Version = fmt.Sprintf("u%d", i), 1
				nodes[i] = n
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
		t.openLT, t.order, t.node = 0, nil, map[string]livedoc.Node{}
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
	if _, ok := t.node[id]; !ok {
		t.order = append(t.order, id)
	}
	n.ID, n.Version = id, t.node[id].Version+1
	t.node[id] = n
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

func (t *livelogTurn) openNodes() []livedoc.Node {
	out := make([]livedoc.Node, 0, len(t.order))
	for _, id := range t.order {
		out = append(out, t.node[id])
	}
	return out
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

// ariaView renders a block by reusing figaro's existing node renderers, so the
// inline-seal output matches the default painter exactly. One representation:
// it renders livedoc.Node directly, no conversion.
type ariaView struct{ settings *renderSettings }

func (v *ariaView) Render(n livedoc.Node, width, tick int) []string {
	switch n.Type {
	case livedoc.NodeTool:
		return renderToolNode(n, width, nodeBashCapDefault, uint64(tick), v.settings != nil && v.settings.verbose)
	case livedoc.NodeThinking:
		return renderThinkingNode(n, width)
	default:
		return renderProseNode(n, width)
	}
}
