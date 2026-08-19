package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/internal/transport"
)

// runFormListen watches an aria's form and draws it as a live JSON tree.
func runFormListen(loaded *config.Loaded, ariaID string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	resolvedID, ep, err := resolveTargetEndpoint(ctx, loaded, acli, ariaID, false, dressing{})
	if err != nil {
		die("%s", err)
	}

	mirror := &formMirror{}
	view := &formView{mirror: mirror, out: os.Stdout, open: map[string]bool{}, aria: resolvedID}

	// Seed from the snapshot, then follow. Reading first and subscribing second
	// would drop whatever landed in between; subscribing first means the seed
	// may be older than a delta already in hand, which the mirror's version
	// check catches and resyncs.
	fcli, err := figaro.DialClient(transport.Endpoint{Scheme: ep.Scheme, Address: ep.Address},
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
				// Said once, and then we stop: refetching per delta would be a
				// storm against a peer that cannot agree with us.
				view.stop(fmt.Sprintf("this daemon speaks form schema %d, this client speaks %d: not tracking",
					d.Schema, rpc.FormDeltaSchema))
			default:
				view.paint()
			}
		})
	if err != nil {
		die("dial aria: %s", err)
	}
	defer fcli.Close()
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

	restore, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		die("form listen needs a terminal: %s", err)
	}
	defer restore()
	fmt.Fprint(os.Stdout, altScreenOn+cursorHide)
	defer fmt.Fprint(os.Stdout, cursorShow+altScreenOff)
	atExit(func() { fmt.Fprint(os.Stdout, cursorShow+altScreenOff) })

	view.paint()
	keys := make([]byte, 8)
	for {
		n, rerr := os.Stdin.Read(keys)
		if rerr != nil || n == 0 {
			return
		}
		switch keys[0] {
		case 'q', 3: // q, C-c
			return
		case 'j':
			view.move(1)
		case 'k':
			view.move(-1)
		case 'e':
			view.page(1)
		case 'd':
			view.page(-1)
		case 'y':
			view.yank()
		case '\r', '\n':
			view.toggle()
		case 27: // an arrow, or a bare escape
			if n >= 3 && keys[1] == '[' {
				switch keys[2] {
				case 'B':
					view.move(1)
				case 'A':
					view.move(-1)
				}
			}
		}
	}
}

// formView is the painter: the rows it last built, where the cursor sits, and
// which branches are open. Every entry point takes the lock, because a delta
// arrives on the notifier's goroutine while a keystroke is being handled.
type formView struct {
	mu      sync.Mutex
	mirror  *formMirror
	out     *os.File
	aria    string
	open    map[string]bool
	rows    []*formNode
	cursor  int
	top     int
	notice  string
	stopped bool
	refetch func() (form.Snapshot, uint64, error)
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
	v.paint()
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
	v.paint()
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
	v.paint()
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
	v.paint()
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
	v.paint()
}

func (v *formView) paint() {
	snap, version, gaps := v.mirror.state()
	width, height := term.Width(), term.Height()
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

	var b []byte
	b = append(b, "\x1b[H\x1b[2J"...)
	head := fmt.Sprintf("form %s · v%d · %d keys · %d rows", v.aria, version, snap.Len(), len(v.rows))
	if gaps > 0 {
		head += fmt.Sprintf(" · %d resync", gaps)
	}
	b = append(b, clipLine(head, width)...)
	b = append(b, "\r\n"...)
	for i := v.top; i < len(v.rows) && i < v.top+body; i++ {
		b = append(b, renderFormRow(v.rows[i], width, i == v.cursor)...)
		b = append(b, "\r\n"...)
	}
	foot := "j/k move · enter expand · y yank · e/d page · q quit"
	if v.notice != "" {
		foot = v.notice + " · " + foot
	}
	b = append(b, "\x1b["+fmt.Sprint(height)+";1H"...)
	b = append(b, clipLine(foot, width)...)
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
