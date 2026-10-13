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

func openFormView(ariaID string, loaded *config.Loaded, onChange func()) (*formView, func(), error) {
	ctx, cancel := context.WithCancel(context.Background())

	spec, intrinsic := splitIntrinsic(ariaID)
	if !knownIntrinsic(intrinsic) {
		cancel()
		return nil, nil, fmt.Errorf("no intrinsic %q (have: %s; %s is the host's own form, and is implied)",
			intrinsic, strings.Join(intrinsicNames(), ", "), intrinsicState)
	}

	acli := mustConnectAngelus(loaded)
	resolvedID, ep, err := resolveTargetEndpoint(ctx, loaded, acli, spec, false, dressing{})
	if err != nil {
		cancel()
		acli.Close()
		return nil, nil, err
	}

	mirror := &formMirror{}
	view := &formView{mirror: mirror, out: os.Stdout, open: map[string]bool{},
		aria: resolvedID, intrinsic: intrinsic}

	// Subscribe before seeding: a seed older than a delta in hand is caught by
	// the version check; the other order drops deltas.
	fcli, err := sdk.DialAria(transport.Endpoint{Scheme: ep.Scheme, Address: ep.Address},
		func(method string, params json.RawMessage) {
			if method != rpc.MethodFormDelta {
				return
			}
			var d rpc.FormDelta
			if json.Unmarshal(params, &d) != nil {
				return
			}
			// Every form of the host rides the same notification.
			if d.Intrinsic != intrinsic {
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
		resp, rerr := fcli.FormOf(rctx, intrinsic)
		if rerr != nil {
			return form.Snapshot{}, 0, rerr
		}
		return resp.Snapshot, resp.Version, nil
	}
	view.resync()
	return view, func() { fcli.Close(); acli.Close(); cancel() }, nil
}

func runFormListen(loaded *config.Loaded, ariaID string) {
	runListen(loaded, ariaID, "", "", true)
}

// formView: a delta arrives on the notifier's goroutine while a key is handled, so
// every entry takes mu.
type formView struct {
	mu        sync.Mutex
	mirror    *formMirror
	out       *os.File
	aria      string
	intrinsic string // "" is the board
	open      map[string]bool
	wrapped   map[string]wrappedLines // an opened value, wrapped once
	wraps     int
	rows      []*formNode
	cursor    int
	top       int
	notice    string
	stopped   bool
	refetch   func() (form.Snapshot, uint64, error)
	closeConn func()
	repaint   func() // the host's; the view never paints itself
}

func (v *formView) changed() {
	if v.repaint != nil {
		v.repaint()
	}
}

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

func (v *formView) Items(width int) []pitRow {
	snap, version, gaps := v.mirror.state()
	if width <= 0 {
		width = 80
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.rows = flattenFormTree(buildFormTree(snap), v.open, nil)

	out := make([]pitRow, 0, len(v.rows)+1)
	if v.notice != "" {
		out = append(out, staticRow(clipLine(v.notice, width)))
	}
	_, _ = version, gaps
	for _, n := range v.rows {
		yank := yankFormNode(n)
		out = append(out, pitRow{text: renderFormRow(n, width, false), yank: yank, id: n.path})
		if !n.leaf() || !v.open[n.path] {
			continue
		}
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

// Items runs on every paint under the render lock: wrap once per value and width,
// and bound it.
const formValueRowsMax = 500

type wrappedLines struct {
	raw []byte // compared, never copied

	width int
	lines []string
}

func (v *formView) wrappedValue(n *formNode, width int) []string {
	if c, ok := v.wrapped[n.path]; ok && c.width == width && bytes.Equal(c.raw, n.value) {
		return c.lines
	}
	v.wraps++
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

// Value rows carry "<path>\x00<n>" so every row has an id of its own.
func valuePath(id string) string {
	if i := strings.IndexByte(id, 0); i >= 0 {
		return id[:i]
	}
	return id
}

// Activate runs under the render lock: it must not repaint.
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
