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
	fcli, err := sdk.DialAria(ep,
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
	view.refetch = func() (*rpc.FormResponse, error) {
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rcancel()
		return fcli.FormOf(rctx, intrinsic)
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
	src       formSource
	out       *os.File
	aria      string
	intrinsic string // "" is the host's own form
	open      map[string]bool
	openRev   uint64
	wrapped   map[string]wrappedLines // an opened value, wrapped once
	wraps     int
	root      *formNode
	rows      []*formNode
	items     []pitRow
	itemsKey  formItemsKey
	rev       uint64
	cursor    int
	top       int
	notice    string
	stopped   bool
	refetch   func() (*rpc.FormResponse, error)
	closeConn func()
	repaint   func() // the host's; the view never paints itself
}

type formSource interface {
	Shown() form.Snapshot
	Version() uint64
}

type formItemsKey struct {
	root    form.Stamp
	openRev uint64
	width   int
	notice  string
}

func (v *formView) source() formSource {
	if v.src != nil {
		return v.src
	}
	if v.mirror != nil {
		return v.mirror
	}
	return &formMirror{}
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
	resp, err := v.refetch()
	if err != nil {
		v.setNotice("resync failed: " + err.Error())
		return
	}
	v.mirror.reset(resp.Snapshot, resp.Incarnation, resp.Version)
	v.changed()
}

func (v *formView) setNotice(text string) {
	v.mu.Lock()
	v.notice = text
	v.mu.Unlock()
}

func (v *formView) Items(width int) []pitRow {
	if width <= 0 {
		width = 80
	}
	snap := v.source().Shown()
	v.mu.Lock()
	defer v.mu.Unlock()
	root := snap.RootNode()
	key := formItemsKey{root: root.Stamp(), openRev: v.openRev, width: width, notice: v.notice}
	if v.items != nil && key == v.itemsKey {
		return v.items
	}
	if v.root == nil {
		v.root = newFormRoot(snap)
	} else {
		v.root.take(root)
		v.root.branch = true
	}
	v.rows = flattenFormTree(v.root, v.open, v.rows[:0])

	out := make([]pitRow, 0, len(v.rows)+1)
	if v.notice != "" {
		out = append(out, staticRow(clipLine(v.notice, width)))
	}
	for _, n := range v.rows {
		n.width = width
		out = append(out, pitRow{textFrom: n, yankFrom: n, id: n.path})
		if !n.leaf() || !v.open[n.path] {
			continue
		}
		indent := strings.Repeat("  ", n.depth)
		for i, line := range v.wrappedValue(n, width-len(indent)-2) {
			out = append(out, pitRow{
				text:     indent + line,
				yankFrom: n,
				id:       fmt.Sprintf("%s\x00%d", n.path, i),
			})
		}
	}
	v.items, v.itemsKey = out, key
	v.rev++
	return out
}

func (v *formView) Revision() uint64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.rev
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
	if c, ok := v.wrapped[n.path]; ok && c.width == width && sameBytes(c.raw, n.value) {
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

func sameBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 || &a[0] == &b[0] {
		return true
	}
	return bytes.Equal(a, b)
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
			v.openRev++
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
