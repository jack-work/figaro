package cli

import (
	"bytes"
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

// openFormView dials an aria, subscribes to its form deltas and returns a view
// that follows them. onChange fires when there is something new to show.
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

// runFormListen is `fig listen` with the form pit open fullscreen, and it is
// nothing else. It used to be a second program -- its own alt screen, raw
// mode, key loop, cursor and window -- and every one of those was a chance to
// disagree with the pager.
func runFormListen(loaded *config.Loaded, ariaID string) {
	runListen(loaded, ariaID, "", "", true)
}

// formView is the model the pit drives: the rows it last built and which
// branches are open. Every entry point takes the lock, because a delta arrives
// on the notifier's goroutine while a keystroke is handled.
type formView struct {
	mu        sync.Mutex
	mirror    *formMirror
	out       *os.File
	aria      string
	open      map[string]bool
	wrapped   map[string]wrappedLines // an opened value, wrapped once
	wraps     int                     // cold wraps, for the test that proves it
	rows      []*formNode
	cursor    int
	top       int
	notice    string
	stopped   bool
	refetch   func() (form.Snapshot, uint64, error)
	closeConn func()
	// repaint is the HOST's: the view never paints itself.
	repaint func()
}

// changed is what an arriving delta calls; key verbs do not (see Activate).
func (v *formView) changed() {
	if v.repaint != nil {
		v.repaint()
	}
}

// stop parks the view for a failure no retry can cure.
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

// resync re-reads the snapshot when a delta does not follow the one before.
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

// Items is the whole of what a form looks like; the pit does the rest. The
// view supplies content and the verbs that are its own, and owns no list.
func (v *formView) Items(width int) []pitRow {
	snap, version, gaps := v.mirror.state()
	if width <= 0 {
		width = 80
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.rows = flattenFormTree(buildFormTree(snap), v.open, nil)

	// NO HEADER: the aria is on the bar, the key count is the list's length,
	// the version is nobody's business until it breaks. A notice still earns a
	// row -- a mirror that has stopped following must say so.
	out := make([]pitRow, 0, len(v.rows)+1)
	if v.notice != "" {
		out = append(out, staticRow(clipLine(v.notice, width)))
	}
	_, _ = version, gaps
	for _, n := range v.rows {
		// Every row is selectable: a form is a list of things you look at.
		yank := yankFormNode(n)
		out = append(out, pitRow{text: renderFormRow(n, width, false), yank: yank, id: n.path})
		if !n.leaf() || !v.open[n.path] {
			continue
		}
		// An opened leaf is its value, spelled out. The rows are selectable so
		// a reader can walk a long one; each yanks the whole value.
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

// wrappedValue is formValueLines, once: Items() runs on every paint, and
// re-wrapping a megabyte there cost 3.5 seconds a frame under the render lock.
// Cached against the value and the width, and BOUNDED -- a megabyte is not a
// list anyone scrolls, it is a thing they yank.
const formValueRowsMax = 500

type wrappedLines struct {
	raw []byte // compared, never copied: string(n.value) per paint is the cost

	width int
	lines []string
}

func (v *formView) wrappedValue(n *formNode, width int) []string {
	if c, ok := v.wrapped[n.path]; ok && c.width == width && bytes.Equal(c.raw, n.value) {
		return c.lines
	}
	v.wraps++ // how many times a value was actually wrapped; the cache's proof
	lines := formValueLines(n.value, width)
	if len(lines) > formValueRowsMax {
		lines = append(lines[:formValueRowsMax:formValueRowsMax],
			AndMore(len(lines)-formValueRowsMax, "lines · y yanks all of it"))
	}
	if v.wrapped == nil {
		v.wrapped = map[string]wrappedLines{}
	}
	v.wrapped[n.path] = wrappedLines{raw: n.value, width: width, lines: lines}
	return lines
}

// valuePath is the key a value row belongs to: value rows carry
// "<path>\x00<n>" so every row has an id of its own.
func valuePath(id string) string {
	if i := strings.IndexByte(id, 0); i >= 0 {
		return id[:i]
	}
	return id
}

// Activate is Enter: open a branch, or spell out a leaf's value. It does NOT
// repaint -- it runs under the render lock, and taking that lock twice froze
// the pager dead. The host repaints after the key it dispatched.
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

// Close releases the subscription and the socket.
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
