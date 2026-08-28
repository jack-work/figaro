package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"os"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/term"
)

// runFormListen watches an aria's form and draws it as a live JSON tree.
// openFormView dials an aria, subscribes to its form deltas and returns a view
// that follows them. onChange fires whenever the view has something new to
// show, which is how a HOST repaints: the shell host paints a screen, the
// drawer host asks the pager to render.
//
// Extracted from runFormListen so the two hosts share one setup. They used to
// be one function that dialled, subscribed, painted and read keys, which is
// why hosting it anywhere else meant copying all four.
func openFormView(ariaID string, loaded *config.Loaded, onChange func()) (*formView, func(), error) {
	ctx, cancel := context.WithCancel(context.Background())

	acli := mustConnectAngelus(loaded)
	resolvedID, ep, err := resolveTargetEndpoint(ctx, loaded, acli, ariaID, false, dressing{})
	if err != nil {
		cancel()
		acli.Close()
		return nil, nil, err
	}

	mirror := &formMirror{}
	view := &formView{mirror: mirror, out: os.Stdout, open: map[string]bool{}, aria: resolvedID}

	// Seed from the snapshot, then follow. Reading first and subscribing second
	// would drop whatever landed in between; subscribing first means the seed
	// may be older than a delta already in hand, which the mirror's version
	// check catches and resyncs.
	fcli, err := sdk.DialAria(transport.Endpoint{Scheme: ep.Scheme, Address: ep.Address},
		func(method string, params json.RawMessage) {
			if method != rpc.MethodFormDelta {
				return
			}
			var d rpc.FormDelta
			if json.Unmarshal(params, &d) != nil {
				return
			}
			switch mirror.apply(d) {
			case formResync:
				view.resync()
			case formIncompatible:
				view.stop(fmt.Sprintf("this daemon speaks form schema %d, this client speaks %d: not tracking",
					d.Schema, rpc.FormDeltaSchema))
			}
			if onChange != nil {
				onChange()
			}
		})
	if err != nil {
		cancel()
		acli.Close()
		return nil, nil, fmt.Errorf("dial aria: %w", err)
	}
	view.refetch = func() (form.Snapshot, uint64, error) {
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rcancel()
		resp, rerr := fcli.Form(rctx)
		if rerr != nil {
			return form.Snapshot{}, 0, rerr
		}
		return resp.Snapshot, resp.Version, nil
	}
	view.resync()
	return view, func() { fcli.Close(); acli.Close(); cancel() }, nil
}

func runFormListen(loaded *config.Loaded, ariaID string) {
	// The pump repaints THIS host; a drawer host passes its own render instead.
	var view *formView
	view, closeView, err := openFormView(ariaID, loaded, func() { view.paint() })
	// The SHELL host paints a screen it owns.
	if err != nil {
		die("%s", err)
	}
	defer closeView()
	view.repaint = view.paint

	restore, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		die("form listen needs a terminal: %s", err)
	}
	defer restore()
	fmt.Fprint(stdout, altScreenOn+cursorHide)
	defer fmt.Fprint(stdout, cursorShow+altScreenOff)
	atExit(func() { fmt.Fprint(stdout, cursorShow+altScreenOff) })

	view.paint()
	keys := make([]byte, 8)
	for {
		n, rerr := os.Stdin.Read(keys)
		if rerr != nil || n == 0 {
			return
		}
		// 'q' and ^C are the HOST's: see formView.Key.
		if keys[0] == 'q' || keys[0] == 3 {
			return
		}
		if view.Key(keys[0]) {
			view.paint()
		}
	}
}

// formView is the painter: the rows it last built, where the cursor sits, and
// which branches are open. Every entry point takes the lock, because a delta
// arrives on the notifier's goroutine while a keystroke is being handled.
type formView struct {
	mu        sync.Mutex
	mirror    *formMirror
	out       *os.File
	aria      string
	open      map[string]bool
	rows      []*formNode
	cursor    int
	top       int
	notice    string
	stopped   bool
	refetch   func() (form.Snapshot, uint64, error)
	closeConn func()
	// repaint is THE HOST's, and every path that changes the view calls it
	// rather than painting itself. Before this, move/page/toggle/yank/resync
	// each called paint() -- which writes to os.Stdout with absolute cursor
	// positioning -- so hosting the view in a drawer put its footer on the
	// pager's status row, twice, from inside a region it did not own.
	repaint func()
}

// changed is what a mutating method calls instead of painting.
func (v *formView) changed() {
	if v.repaint != nil {
		v.repaint()
	}
}

// stop parks the view: the notice stays, and nothing is applied or refetched
// after it. Reserved for a failure no retry can cure.
func (v *formView) stop(reason string) {
	v.mu.Lock()
	if v.stopped {
		v.mu.Unlock()
		return
	}
	v.stopped, v.notice = true, reason
	v.mu.Unlock()
	v.changed()
}

// resync re-reads the snapshot. The mirror asks for this when a delta does not
// follow the one before it, which is the only honest answer to a gap.
func (v *formView) resync() {
	v.mu.Lock()
	stopped := v.stopped
	v.mu.Unlock()
	if v.refetch == nil || stopped {
		return
	}
	snap, version, err := v.refetch()
	if err != nil {
		v.setNotice("resync failed: " + err.Error())
		return
	}
	v.mirror.reset(snap, version)
	v.changed()
}

func (v *formView) setNotice(text string) {
	v.mu.Lock()
	v.notice = text
	v.mu.Unlock()
}

func (v *formView) move(delta int) {
	v.mu.Lock()
	v.cursor = clampInt(v.cursor+delta, 0, len(v.rows)-1)
	v.mu.Unlock()
	v.changed()
}

func (v *formView) page(delta int) {
	height := term.Height()
	v.move(delta * max(1, height-4))
}

func (v *formView) toggle() {
	v.mu.Lock()
	if v.cursor < len(v.rows) {
		openBranch(v.rows[v.cursor], v.open)
	}
	v.mu.Unlock()
	v.changed()
}

func (v *formView) yank() {
	v.mu.Lock()
	var text string
	if v.cursor < len(v.rows) {
		text = yankFormNode(v.rows[v.cursor])
	}
	v.mu.Unlock()
	if text == "" {
		return
	}
	copyToClipboard(v.out, text)
	v.setNotice(fmt.Sprintf("yanked %d bytes", len(text)))
	v.changed()
}

// Rows renders the view for a viewport, and positions nothing. It is the whole
// of what the view LOOKS like, so a host -- a full screen at a shell, a drawer
// in the pager -- can put those rows wherever it owns.
//
// This used to be paint(): one function that decided the content AND wrote it
// to a *os.File with \x1b[H\x1b[2J and an absolute jump to the last line. That
// is why `form listen` could not be hosted: it did not render, it TOOK a
// screen.
// Items is how a form takes part in THE PIT: it hands over rows and stops
// owning a list. It used to keep its own rows, cursor and top and its own j/k
// handler -- a fourth implementation beside the picker -- and it computed its
// window as height-2 while the pit reserved its own, so the cursor and the
// visible range disagreed and the highlight appeared to skip entries.
//
// Now the view supplies content and the two VERBS that are its own (Enter
// expands a branch, y yanks a value); everything a finger does to move is the
// picker's, once, for every pit.
func (v *formView) Items(width int) []drawerRow {
	snap, version, gaps := v.mirror.state()
	if width <= 0 {
		width = 80
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.rows = flattenFormTree(buildFormTree(snap), v.open, nil)

	head := fmt.Sprintf("form %s · v%d · %d keys · %d rows", v.aria, version, snap.Len(), len(v.rows))
	if gaps > 0 {
		head += fmt.Sprintf(" · %d resync", gaps)
	}
	out := make([]drawerRow, 0, len(v.rows)+1)
	out = append(out, staticRow(clipLine(head, width)))
	for _, n := range v.rows {
		// EVERY ROW IS SELECTABLE, which is the other half of the bug: a form's
		// child rows were not, so a motion that stepped to the next selectable
		// row jumped over whole properties. A form is a list of things you
		// look at; all of them can hold the cursor.
		out = append(out, drawerRow{
			text: renderFormRow(n, width, false),
			yank: yankFormNode(n),
			id:   n.path,
		})
	}
	return out
}

// Activate is Enter on a row: expand or collapse that branch.
func (v *formView) Activate(path string) {
	v.mu.Lock()
	for _, n := range v.rows {
		if n.path == path {
			openBranch(n, v.open)
			break
		}
	}
	v.mu.Unlock()
	v.changed()
}

func (v *formView) Rows(width, height int) []string {
	snap, version, gaps := v.mirror.state()
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.rows = flattenFormTree(buildFormTree(snap), v.open, nil)
	v.cursor = clampInt(v.cursor, 0, len(v.rows)-1)
	// Keep the cursor on screen without scrolling further than it moved.
	body := max(1, height-2)
	if v.cursor < v.top {
		v.top = v.cursor
	}
	if v.cursor >= v.top+body {
		v.top = v.cursor - body + 1
	}
	v.top = clampInt(v.top, 0, max(0, len(v.rows)-1))

	head := fmt.Sprintf("form %s · v%d · %d keys · %d rows", v.aria, version, snap.Len(), len(v.rows))
	if gaps > 0 {
		head += fmt.Sprintf(" · %d resync", gaps)
	}
	out := make([]string, 0, body+1)
	out = append(out, clipLine(head, width))
	for i := v.top; i < len(v.rows) && i < v.top+body; i++ {
		out = append(out, renderFormRow(v.rows[i], width, i == v.cursor))
	}
	return out
}

// Hint is what the view can do, for the HOST to place. It is not a row: the
// shell host puts it on the last line of a screen it owns, and the drawer puts
// it on its closing rule -- and when Rows carried it itself, both hosts drew
// it AND the drawer drew its own, so the affordance appeared twice.
func (v *formView) Hint() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	hint := "j/k move · Enter expand · y yank · e/d page"
	if v.notice != "" {
		return v.notice + " · " + hint
	}
	return hint
}

// Key offers one keystroke, and reports whether the view took it. 'q' and ^C
// are NOT taken: leaving is the host's decision -- at a shell it ends the
// command, in a drawer it closes the drawer -- and a view that swallowed them
// would be a view you could not get out of in either place.
func (v *formView) Key(b byte) bool {
	switch b {
	case 'j':
		v.move(1)
	case 'k':
		v.move(-1)
	case 'e':
		v.page(1)
	case 'd':
		v.page(-1)
	case '\r', '\n':
		v.toggle()
	case 'y':
		v.yank()
	default:
		return false
	}
	return true
}

// Close is the LiveView half of teardown; the connection belongs to whoever
// dialled it, so there is nothing here yet.
func (v *formView) Close() {
	if v.closeConn != nil {
		v.closeConn()
	}
}

// paint is the SHELL host: the same rows, positioned on a screen this process
// owns. The pager's host is drawer-shaped and lives in transcript.go.
func (v *formView) paint() {
	width, height := term.Width(), term.Height()
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	rows := v.Rows(width, height)
	var b []byte
	b = append(b, "\x1b[H\x1b[2J"...)
	for _, r := range rows {
		b = append(b, r...)
		b = append(b, "\r\n"...)
	}
	// The shell host owns a whole screen, so it puts the hint on the last line.
	b = append(b, "\x1b["+fmt.Sprint(height)+";1H"...)
	b = append(b, clipLine(v.Hint()+" · q quit", width)...)
	_, _ = v.out.Write(b)
}

func clipLine(s string, width int) string {
	if len(s) <= width {
		return s
	}
	return s[:max(0, width-1)] + "…"
}

func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
