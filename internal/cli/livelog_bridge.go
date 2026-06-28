package cli

import (
	"io"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// livelogTurn renders the aria-read wire through the inline-seal renderer. It
// folds each AriaRead with an aria.Client and drives the renderer from the
// client's hooks: a closed message seals to native scrollback once (and is never
// redrawn); the open message is the one live region, so a terminal resize
// repaints just that bounded part — the structural fix for the resize/dup class.
type livelogTurn struct {
	in     *ldrender.Inline
	term   *ldrender.ANSITerminal
	client *aria.Client

	openLT int
	open   []livedoc.Node
}

func newLivelogTurn(out io.Writer, w, h int, settings *renderSettings, bookend func() string) *livelogTurn {
	term := ldrender.NewANSITerminal(out, w, h)
	in := ldrender.NewInline(term, &ariaView{settings: settings})
	in.Bookend = bookend
	t := &livelogTurn{in: in, term: term, client: aria.NewClient()}
	t.client.OnClosed = func(m aria.Message) { t.in.Seal(m) }
	t.client.OnLive = func(lt int, role string, nodes []livedoc.Node) {
		t.openLT, t.open = lt, nodes
		t.in.Open(lt, role, nodes)
	}
	return t
}

func (t *livelogTurn) apply(r aria.AriaRead) { t.client.Apply(r) }
func (t *livelogTurn) tick()                 { t.in.Tick(t.open) }
func (t *livelogTurn) resize(w, h int)       { t.term.SetSize(w, h); t.in.Resize(t.open) }
func (t *livelogTurn) cursor() int           { return t.client.Cursor() }

// render re-paints the open message (e.g. after a verbosity toggle).
func (t *livelogTurn) render() {
	if t.openLT != 0 {
		t.in.Open(t.openLT, "assistant", t.open)
	}
}

// ariaView renders a block by reusing figaro's existing node renderers, so the
// inline-seal output matches what the renderers always produced. One
// representation: it renders livedoc.Node directly, no conversion.
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
