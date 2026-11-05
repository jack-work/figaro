package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/sdk"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/form/replica"
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
	kick := newFormKicker(onChange)
	var rep atomic.Pointer[replica.Replica]

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
			if r := rep.Load(); r != nil {
				r.Delta(d)
				return
			}
			switch mirror.apply(d) {
			case formResync:
				view.resync()
			case formIncompatible:
				view.stop(fmt.Sprintf("this daemon speaks form schema %d, this client speaks %d: not tracking",
					d.Schema, rpc.FormDeltaSchema))
			}
			kick.kick()
		})
	if err != nil {
		cancel()
		acli.Close()
		kick.close()
		return nil, nil, fmt.Errorf("dial aria: %w", err)
	}
	if !formPitEditable(intrinsic) {
		view.refetch = func() (*rpc.FormResponse, error) {
			rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer rcancel()
			return fcli.FormOf(rctx, intrinsic)
		}
		view.resync()
		return view, func() { fcli.Close(); acli.Close(); cancel(); kick.close() }, nil
	}

	r := replica.New(newReplicaClient(), ariaTransport{aria: fcli, loaded: view.present}, replica.Options{
		Debounce: formEditDebounce,
		MaxWait:  formEditMaxWait,
		OnChange: func(_, _ form.Snapshot) { kick.kick() },
		OnIncompatible: func(schema int) {
			go view.stop(fmt.Sprintf("this daemon speaks form schema %d, this client speaks %d: not tracking",
				schema, rpc.FormDeltaSchema))
		},
		OnRefused: func(_ uint64, err error) {
			view.refuse(err)
			kick.kick()
		},
	})
	rep.Store(r)
	view.src = replicaSource{r}
	go stopPitWhenLinkDies(fcli.Done(), ctx.Done(), view, r)
	notePit(resolvedID, r.Pending)
	select {
	case <-r.Ready():
	case <-time.After(5 * time.Second):
		view.setNotice("the form has not loaded yet")
	}
	return view, func() {
		if r.Pending() > 0 {
			fctx, fcancel := context.WithTimeout(context.Background(), formFlushOnClose)
			_ = r.Flush(fctx)
			fcancel()
		}
		r.Close()
		fcli.Close()
		acli.Close()
		cancel()
		kick.close()
	}, nil
}

var pitLedger struct {
	sync.Mutex
	pits []pitEntry
}

type pitEntry struct {
	form    string
	pending func() int
}

func notePit(form string, pending func() int) {
	pitLedger.Lock()
	defer pitLedger.Unlock()
	pitLedger.pits = append(pitLedger.pits, pitEntry{form: form, pending: pending})
}

func reportUnansweredPitEdits(w io.Writer) {
	pitLedger.Lock()
	pits := pitLedger.pits
	pitLedger.pits = nil
	pitLedger.Unlock()
	for _, p := range pits {
		if n := p.pending(); n > 0 {
			fmt.Fprintf(w, "%d form edit(s) to %s were not confirmed\n", n, p.form)
		}
	}
}

func stopPitWhenLinkDies(link, closing <-chan struct{}, view *formView, r *replica.Replica) {
	select {
	case <-link:
	case <-closing:
		return
	}
	unanswered := r.Pending()
	r.Close()
	note := "this form's connection closed: reopen the form to edit again"
	if unanswered > 0 {
		note = fmt.Sprintf("this form's connection closed with %d edit(s) unanswered: reopen the form to see what landed", unanswered)
	}
	view.stop(note)
}

func formPitEditable(intrinsic string) bool {
	return intrinsic == "" && !daemonLacks(rpc.CapFormSeq)
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
	refused   bool
	touched   map[string]bool // paths under an edit the server has not answered
	stopped   bool
	access    *rpc.FormAccess
	view      *rpc.FormView
	refetch   func() (*rpc.FormResponse, error)
	closeConn func()
	repaint   func() // the host's; the view never paints itself
	change    func() // the host's: fill its : line from the selected row
	act       func(rpc.ViewAction)
	subject   func() form.Snapshot
	rowKeys   map[string]viewRowKey
	seeded    map[string]bool
	armed     string
	search    *formSearch
}

type formSource interface {
	Shown() form.Snapshot
	Version() uint64
}

type formEditor interface {
	Edit(form.Patch) error
}

type formItemsKey struct {
	root      form.Stamp
	subject   form.Stamp
	confirmed form.Stamp
	openRev   uint64
	width     int
	notice    string
	pending   int
	search    uint64
}

type pendingReader interface {
	Read() (confirmed, shown form.Snapshot, pending int)
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
	v.present(resp)
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
	src := v.source()
	var confirmed, snap form.Snapshot
	pending := 0
	if pr, ok := src.(pendingReader); ok {
		confirmed, snap, pending = pr.Read()
	} else {
		snap = src.Shown()
	}
	var subjectStamp form.Stamp
	if v.subject != nil {
		subjectStamp = v.subject().RootNode().Stamp()
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	root := snap.RootNode()
	key := formItemsKey{root: root.Stamp(), subject: subjectStamp, confirmed: confirmed.RootNode().Stamp(), openRev: v.openRev, width: width, notice: v.notice, pending: pending}
	if v.search != nil {
		key.search = v.search.rev
	}
	if v.items != nil && key == v.itemsKey {
		return v.items
	}
	v.touched = nil
	if pending > 0 {
		v.touched = touchedPaths(snap.Diff(confirmed))
	}
	if v.root == nil {
		v.root = newFormRoot(snap)
	} else {
		v.root.take(root)
		v.root.branch = true
	}
	if v.searching() {
		v.items = v.searchItems(snap, width)
		key.search = v.search.rev
		v.itemsKey = key
		v.rev++
		return v.items
	}
	var shape *viewShape
	if v.view != nil {
		if v.seeded == nil {
			v.seeded = map[string]bool{}
		}
		shape = &viewShape{hint: v.view, seeded: v.seeded, keys: map[string]viewRowKey{}, heads: map[string]viewRowKey{}}
		if v.subject != nil {
			shape.subject = v.subject()
		}
	}
	w := guideWalk{open: v.open, out: v.rows[:0], shape: shape}
	w.walk(v.root, "", true, nil, nil)
	v.rows = w.out
	v.rowKeys = nil
	hint := ""
	if shape != nil {
		v.rowKeys = shape.keys
		hint = shape.actionHint()
	}

	out := make([]pitRow, 0, len(v.rows)+1)
	if v.notice != "" {
		out = append(out, staticRow(clipLine(v.notice, width)))
	}
	for _, n := range v.rows {
		n.width = width
		row := pitRow{textFrom: n, yankFrom: n, id: n.path}
		if v.touched[n.path] {
			row.note = "pending"
		} else if n.leaf() && v.readOnlyLocked(n.path) {
			row.note = "read-only"
		}
		if k, ok := shape.head(n.path); ok {
			if shape.marked(n, k) {
				row.note = "●"
			}
			row.hint = hint
		}
		out = append(out, row)
		if !n.leaf() || !v.open[n.path] {
			continue
		}
		indent := n.cont + "   "
		for i, line := range v.wrappedValue(n, width-displayWidth(indent)-2) {
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

func (v *formView) readOnlyLocked(path string) bool {
	if v.access == nil {
		return false
	}
	if !v.access.Writable() {
		return true
	}
	_, denied := v.access.Refusal(path)
	return denied
}

func touchedPaths(p form.Patch) map[string]bool {
	out := map[string]bool{}
	for _, ent := range p.Entries() {
		for path := ent.Key; path != "" && !out[path]; {
			out[path] = true
			i := strings.LastIndexByte(path, '.')
			if i < 0 {
				break
			}
			path = path[:i]
		}
	}
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
