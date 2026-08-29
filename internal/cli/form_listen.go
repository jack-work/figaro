package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/config"
)

// runFormListen watches an aria's form and draws it as a live JSON tree.
// openFormView dials an aria, subscribes to its form deltas and returns a view
// that follows them. onChange fires whenever the view has something new to
// show, which is how a HOST repaints: the shell host paints a screen, the
// pit host asks the pager to render.
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

// runFormListen is `fig listen` WITH THE FORM PIT OPEN, fullscreen, and it is
// nothing else.
//
// It used to be a second program: its own alt screen, its own raw mode, its own
// key loop, its own cursor and window over the same rows the pit already knows
// how to draw. Every one of those was a chance to disagree with the pager --
// and it took every one: a window computed as height-2 against a pit that
// reserved its own, a cursor that skipped rows the pit could hold, keys that
// meant one thing here and another there. Gluck, 2026-08-28: "all of the custom
// fig form show UI should just be deleted outright. removed. killed."
//
// So there is one program. `fig form listen` opens the transcript with the form
// in the pit at full height; Esc closes the pit and leaves the conversation
// where it was; F brings the pit back to its ordinary twelve rows.
func runFormListen(loaded *config.Loaded, ariaID string) {
	runListen(loaded, ariaID, "", "", true)
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
	wrapped   map[string]wrappedLines // an opened value, wrapped once
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
	// positioning -- so hosting the view in a pit put its footer on the
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

// Items is THE WHOLE OF WHAT A FORM LOOKS LIKE, and the pit does the rest.
//
// Items is how a form takes part in THE PIT: it hands over rows and stops
// owning a list. It used to keep its own rows, cursor and top and its own j/k
// handler -- a fourth implementation beside the picker -- and it computed its
// window as height-2 while the pit reserved its own, so the cursor and the
// visible range disagreed and the highlight appeared to skip entries.
//
// Now the view supplies content and the two VERBS that are its own (Enter
// expands a branch, y yanks a value); everything a finger does to move is the
// picker's, once, for every pit.
func (v *formView) Items(width int) []pitRow {
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
	// THE NOTICE RIDES ON THE HEAD ROW. It used to have a footer of its own,
	// drawn by a view that owned a screen; there is no screen and no footer
	// now, and a resync failure that is reported nowhere is a form quietly
	// telling you nothing while showing you something stale.
	if v.notice != "" {
		head += " · " + v.notice
	}
	out := make([]pitRow, 0, len(v.rows)+1)
	out = append(out, staticRow(clipLine(head, width)))
	for _, n := range v.rows {
		// EVERY ROW IS SELECTABLE, which is the other half of the bug: a form's
		// child rows were not, so a motion that stepped to the next selectable
		// row jumped over whole properties. A form is a list of things you
		// look at; all of them can hold the cursor.
		yank := yankFormNode(n)
		out = append(out, pitRow{text: renderFormRow(n, width, false), yank: yank, id: n.path})
		if !n.leaf() || !v.open[n.path] {
			continue
		}
		// AN OPENED LEAF IS ITS VALUE, spelled out. The rows are selectable
		// too -- every one of them yanks the WHOLE value, and a cursor that
		// can rest on them is what lets a reader scroll through a long one
		// instead of watching it fly past. The id keeps the row addressable
		// and unique, which is what a refresh restores the selection by.
		indent := strings.Repeat("  ", n.depth)
		for i, line := range v.wrappedValue(n, width-len(indent)-2) {
			out = append(out, pitRow{
				text: indent + line,
				yank: yank,
				id:   fmt.Sprintf("%s\x00%d", n.path, i),
			})
		}
	}
	return out
}

// wrappedValue is formValueLines, ONCE. Items() runs on every paint, and a
// value is wrapped at a width that changes only when the pane does, so wrapping
// it again on every frame was pure cost -- 3.5 seconds a frame on a megabyte,
// with the render lock held. Cached against the value itself, so a `set` that
// changes it re-wraps and nothing else does.
//
// AND IT IS BOUNDED. A megabyte of value is not a list a reader scrolls; it is
// a thing they yank (y takes the whole value, from any of its rows). Past the
// cap the pit says how much it is not showing, in the same words every other
// truncation in the program uses.
const formValueRowsMax = 500

type wrappedLines struct {
	raw   string
	width int
	lines []string
}

func (v *formView) wrappedValue(n *formNode, width int) []string {
	raw := string(n.value)
	if c, ok := v.wrapped[n.path]; ok && c.width == width && c.raw == raw {
		return c.lines
	}
	lines := formValueLines(n.value, width)
	if len(lines) > formValueRowsMax {
		lines = append(lines[:formValueRowsMax:formValueRowsMax],
			AndMore(len(lines)-formValueRowsMax, "lines · y yanks all of it"))
	}
	if v.wrapped == nil {
		v.wrapped = map[string]wrappedLines{}
	}
	v.wrapped[n.path] = wrappedLines{raw: raw, width: width, lines: lines}
	return lines
}

// valuePath is the key a value row belongs to: an opened leaf's lines carry
// "<path>\x00<n>" so that every row in the pit has an id of its own, and Enter
// on any of them closes the value it came from.
func valuePath(id string) string {
	if i := strings.IndexByte(id, 0); i >= 0 {
		return id[:i]
	}
	return id
}

// Activate is Enter on a row: open what is under it. On a branch that is its
// children; on a leaf it is the VALUE, spelled out over as many rows as it
// takes and pretty-printed when it parses as JSON -- which is how a form's
// biggest values arrive, a skill's frontmatter or a serialised file. Enter on
// one of those value rows closes it again.
//
// IT DOES NOT REPAINT, and that is not an oversight. Activate is called from
// the pit's key dispatch, which runs WITH THE RENDER LOCK HELD -- the input
// loop takes that lock around every keystroke -- so a repaint from in here is
// the pager taking a mutex it is already holding. Go's mutexes do not recurse:
// the pager froze, dead, with the form still on screen. (Measured in a pty the
// moment the pit's selection became visible to this verb at all; before that,
// selected() answered false for a hosted view and Enter never arrived.)
//
// The host repaints after every key it dispatches. A verb that mutates and
// returns is all a hosted view owes it.
func (v *formView) Activate(id string) {
	path := valuePath(id)
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, n := range v.rows {
		if n.path == path {
			openBranch(n, v.open)
			return
		}
	}
}

// Close is the LiveView half of teardown; the connection belongs to whoever
// dialled it, so there is nothing here yet.
func (v *formView) Close() {
	if v.closeConn != nil {
		v.closeConn()
	}
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
