package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/rpc"
	lddoc "github.com/jack-work/figaro/internal/livelog/doc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// livelogTurn renders a turn through the livelog module (pi-style differential
// rendering that owns its screen) instead of the inline liveRegion painter. It
// is opt-in via FIGARO_LIVELOG and runs in the alternate screen, which is what
// lets it full-redraw cleanly on a terminal resize — the case that duplicated
// content under the inline painter.
//
// It translates figaro's positional wire ops into livelog doc events and reuses
// figaro's own node renderers (glamour prose, tool widgets) via figaroView, so
// content looks identical; only the cursor/diff/resize machinery is livelog's.
type livelogTurn struct {
	d        *lddoc.Doc
	r        *ldrender.Renderer
	term     *ldrender.ANSITerminal
	view     *figaroView
	unit     int // bumped per snapshot so block IDs are unique across units
	settings *renderSettings
}

func newLivelogTurn(out io.Writer, w, h int, settings *renderSettings, bookend func() string) *livelogTurn {
	t := ldrender.NewANSITerminal(out, w, h)
	v := &figaroView{settings: settings}
	r := ldrender.New(t, v)
	r.Bookend = bookend
	return &livelogTurn{d: lddoc.New(), r: r, term: t, view: v, settings: settings}
}

// handle decodes a wire notification and folds it into the document, repainting.
// Returns true if it consumed the method (turn.done is left to the caller).
func (lt *livelogTurn) handle(method string, params json.RawMessage) bool {
	switch method {
	case rpc.MethodLogSnapshot:
		var e rpc.SnapshotEntry
		if json.Unmarshal(params, &e) == nil {
			lt.unit++
			for i, n := range e.Nodes {
				lt.d.Apply(lddoc.Append(lt.block(i, n)))
			}
			lt.render()
		}
	case rpc.MethodNodeOpen:
		var e rpc.NodeOpenEntry
		if json.Unmarshal(params, &e) == nil {
			lt.d.Apply(lddoc.Append(lt.block(e.Index, e.Node)))
			lt.render()
		}
	case rpc.MethodNodePatch:
		var e rpc.NodePatchEntry
		if json.Unmarshal(params, &e) == nil {
			// figaro nodes carry a single streamed field; route output|markdown to Body.
			lt.d.Apply(lddoc.Patch(lt.id(e.Index), lddoc.Delta{At: e.At, Del: e.Del, Ins: e.Ins}))
			lt.render()
		}
	case rpc.MethodNodeSet:
		var e rpc.NodeSetEntry
		if json.Unmarshal(params, &e) == nil {
			id := lt.id(e.Index)
			lt.d.Apply(lddoc.SetStatus(id, statusOf(e.Status)))
			attrs := map[string]string{"fstatus": e.Status}
			if e.Name != "" {
				attrs["name"] = e.Name
			}
			if e.Args != nil {
				if a, err := json.Marshal(e.Args); err == nil {
					attrs["args"] = string(a)
				}
			}
			lt.d.Apply(lddoc.SetAttrs(id, attrs))
			lt.render()
		}
	case rpc.MethodLogCommit:
		lt.d.Apply(lddoc.Seal())
		lt.render()
	default:
		return false
	}
	return true
}

func (lt *livelogTurn) tick()           { lt.r.Tick() }
func (lt *livelogTurn) resize(w, h int) { lt.term.SetSize(w, h); lt.render() }
func (lt *livelogTurn) render()         { lt.r.Render(lt.d.Blocks()) }

func (lt *livelogTurn) id(i int) string { return fmt.Sprintf("u%d-%d", lt.unit, i) }

func (lt *livelogTurn) block(i int, n livedoc.Node) lddoc.Block {
	body := n.Markdown
	if n.Type == livedoc.NodeTool {
		body = n.Output
	}
	attrs := map[string]string{"name": n.Name, "fstatus": n.Status}
	if a, err := json.Marshal(n.Args); err == nil {
		attrs["args"] = string(a)
	}
	return lddoc.Block{ID: lt.id(i), Kind: kindOf(n.Type), Status: statusOf(n.Status), Body: body, Attrs: attrs}
}

// finalText renders the settled document to plain rows for printing to the
// normal screen after the alternate screen is torn down, so the result persists
// in scrollback.
func (lt *livelogTurn) finalText(width int) string {
	var b strings.Builder
	for bi, blk := range lt.d.Blocks() {
		if bi > 0 {
			b.WriteByte('\n')
		}
		for _, l := range lt.view.Render(blk, width, 0) {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// figaroView is the livelog BlockRenderer that reuses figaro's existing node
// renderers, so the on-screen content is identical to the inline painter's.
type figaroView struct{ settings *renderSettings }

func (v *figaroView) Render(b lddoc.Block, width, tick int) []string {
	n := livedoc.Node{Type: typeOf(b.Kind), Name: b.Attrs["name"], Status: b.Attrs["fstatus"]}
	if s := b.Attrs["args"]; s != "" && s != "null" {
		_ = json.Unmarshal([]byte(s), &n.Args)
	}
	switch n.Type {
	case livedoc.NodeTool:
		n.Output = b.Body
		expand := v.settings != nil && v.settings.verbose
		return renderToolNode(n, width, nodeBashCapDefault, uint64(tick), expand)
	case livedoc.NodeThinking:
		n.Markdown = b.Body
		return renderThinkingNode(n, width)
	default:
		n.Markdown = b.Body
		return renderProseNode(n, width)
	}
}

func kindOf(t livedoc.NodeType) string {
	switch t {
	case livedoc.NodeTool:
		return "tool"
	case livedoc.NodeThinking:
		return "thinking"
	default:
		return "prose"
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

func statusOf(figaroStatus string) lddoc.Status {
	switch figaroStatus {
	case livedoc.StatusOK:
		return lddoc.StatusOK
	case livedoc.StatusError:
		return lddoc.StatusError
	case livedoc.StatusRunning:
		return lddoc.StatusActive
	default:
		return ""
	}
}

const (
	altScreenOn  = "\x1b[?1049h" // enter the alternate screen buffer
	altScreenOff = "\x1b[?1049l" // leave it, restoring the normal screen
)
