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
//
// It holds its OWN copy of the state (formMirror) and patches it with what the
// server broadcasts, rather than re-reading a snapshot per frame. That is the
// point of the exercise: the same tree, the same algebra, the same patches, on
// both ends of a socket. A web UI is the same client with a different painter.
//
//	j / k     move
//	enter     expand / collapse
//	y         yank (OSC 52, so it survives ssh and tmux)
//	e / d     page down / up
//	q, C-c    leave
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

	restore, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		die("form listen needs a terminal: %s", err)
	}
	// The MODE, like the screen, has to survive exitNow -- which runs the hooks
	// and then os.Exit, skipping every defer. stream.go:330 documents this one
	// as measured: a Ctrl-C 130 left c_lflag at a30, so the user got their shell
	// back with no echo and no line editing. Nothing calls exitNow inside this
	// window today; leaving the worse of the two failures on a bare defer is
	// still not worth the line it saves. sync.OnceFunc because both paths fire
	// on a normal return.
	restoreOnce := sync.OnceFunc(restore)
	defer restoreOnce()
	fmt.Fprint(os.Stdout, altScreenOn+cursorHide)
	view.begin()
	defer func() {
		// end() BEFORE the screen is put back: a delta arriving during teardown
		// would otherwise paint onto the terminal we have just restored.
		view.end()
		fmt.Fprint(os.Stdout, cursorShow+altScreenOff)
	}()
	atExit(func() {
		view.end()
		fmt.Fprint(os.Stdout, cursorShow+altScreenOff)
		restoreOnce()
	})

	// Seeded here, not before the switch: resync ends in a paint, and a paint on
	// the primary screen erases what the user was reading.
	view.resync()

	// Unconditional, and NOT redundant with the resync above: an incompatible
	// schema delta can land any time after DialClient, including before begin().
	// stop() paints that notice while the view is not yet live, and resync()
	// returns early once stopped -- so this is the only thing that ever renders
	// a notice set before the screen was switched.
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

	// live gates every paint on the alternate screen being ON.
	//
	// paint() begins with erase-in-display and ends by parking the cursor on the
	// last row. On the alternate screen that is free; on the PRIMARY screen it
	// erases whatever the user was looking at and leaves a screenful of blank
	// lines behind, which is what they find when they quit and the alternate
	// screen is put back.
	//
	// Two paints could land there. The seeding resync ran before the terminal
	// was switched, and a delta can arrive on the notifier's goroutine at any
	// moment -- including after the alternate screen has been put away and
	// before the process has actually exited.
	live bool
}

// begin marks the view paintable. Call it AFTER the alternate screen is on.
func (v *formView) begin() {
	v.mu.Lock()
	v.live = true
	v.mu.Unlock()
}

// end marks the view unpaintable. Call it BEFORE the alternate screen is put
// away, so a delta racing the teardown cannot paint onto the restored screen.
func (v *formView) end() {
	v.mu.Lock()
	v.live = false
	v.mu.Unlock()
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
	if !v.live {
		// Not on the alternate screen: painting here would erase the user's own
		// terminal. See formView.live.
		return
	}
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
