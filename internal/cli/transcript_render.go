package cli

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/livedoc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

func (t *transcript) invalidateRows() {
	t.rowRev++
	t.rowCache = map[sliceKey]cachedMessage{}
	// Whatever voided the committed rows voided the open message's blocks too.
	t.openMemo = openMemo{}
}

// screenMoved: something else wrote to the screen, so the next frame repaints in full.
func (t *transcript) screenMoved() { t.prev = nil }

func (t *transcript) resyncDue() bool {
	if transcriptResyncInterval <= 0 {
		return false
	}
	if t.now == nil {
		t.now = time.Now
	}
	return t.now().Sub(t.lastFull) >= transcriptResyncInterval
}

// Unchanged rows are usually the same string values, so most comparisons are a
// pointer and a length.
func screensEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (t *transcript) lines() []string {
	t.buildIndex()
	t.lineBuf = t.window(0, t.index.total, t.lineBuf)
	return t.lineBuf
}

// Memoized: strings.Repeat per message per frame is 16 KB/frame of identical strings.
func (t *transcript) transRule() string {
	if t.ruleStr == "" || t.ruleW != t.w {
		t.ruleStr, t.ruleW = dimTransRule(t.w), t.w
	}
	return t.ruleStr
}

func (t *transcript) renderMsgBase(m aria.Message) cachedMessage {
	return t.renderMsg(m, nil)
}

// Markdown layout is the most expensive thing the pager does and the open message
// is redrawn every frame: the memo makes a frame cost what the stream changed.
func (t *transcript) renderOpenMsg(m aria.Message) cachedMessage {
	if t.openMemo.key != keyOf(m) || t.openMemo.w != t.w || t.openMemo.blocks == nil {
		t.openMemo = openMemo{key: keyOf(m), w: t.w, blocks: map[int]*memoBlock{}}
	}
	for _, b := range t.openMemo.blocks {
		b.reused = false
	}
	return t.renderMsg(m, &t.openMemo)
}

func (t *transcript) renderMsg(m aria.Message, memo *openMemo) cachedMessage {
	c := t.rowComposer(m)
	if memo != nil {
		c.Memo = memo.compose
	}
	composed := c.Message(m, t.w)
	rows := make([]transcriptRow, 0, len(composed))
	for i := 0; i < len(composed); {
		r := composed[i]
		if r.Block == ldrender.BlockChrome {
			rows = append(rows, transcriptRow{text: r.Text, more: r.More})
			i++
			continue
		}
		if memo != nil {
			if kept, ok := memo.kept(r.Block); ok {
				rows = append(rows, kept...)
				for i < len(composed) && composed[i].Block == r.Block {
					i++
				}
				continue
			}
		}
		block := r.Block
		start := len(rows)
		for ; i < len(composed) && composed[i].Block == block; i++ {
			rows = append(rows, t.pagerRow(m, composed[i]))
		}
		if memo != nil {
			memo.keep(block, rows[start:len(rows):len(rows)])
		}
	}
	return cachedMessage{rows: rows}
}

func (t *transcript) pagerRow(m aria.Message, r ldrender.Row) transcriptRow {
	ref := blockRef(m, r.Block)
	if r.Meta > 0 {
		ref = replyRefOf(metaRefOf(ref, r.Meta), r.Reply)
	}
	// Stored clipped (plainNodeRow) so an untouched frame allocates nothing;
	// sgrCollapse strips the per-cell rendition churn glamour emits, 3/4 of
	// the row bytes.
	return transcriptRow{
		text:   sgrCollapse(plainNodeRow(r.Text, t.w)),
		ref:    ref,
		mark:   r.Mark,
		gutter: r.Gutter,
		spine:  r.Spine,
		chrome: r.Chrome,
		more:   r.More,
	}
}

func (t *transcript) rowComposer(m aria.Message) ldrender.Composer {
	c := ldrender.Composer{
		// The pager view may open arguments as well as output
		// (ariaView.gesture).
		View: pagerView(t.view), Header: messageHeader, Rule: t.transRule, InputHeader: inputHeader, HeadMore: headMore, Tick: t.tick,
		Expanded: func(block int) bool { return t.expanded[blockRef(m, block)] },
		Adorn: func(block int, n livedoc.Node, deltas map[string]livedoc.FormDelta, w int) ldrender.Adornment {
			return buildAdornment(adornerFor(block, n), t.section(m, block, n, deltas), w)
		},
		Quote: quoteRows,
	}
	// The address is composed always and drawn only under M-m: the toggle is a
	// paint-time decision and the row cache does not know it.
	layout := t.coordFormat()
	c.Mark = func(block int, n livedoc.Node) string {
		ref := blockRef(m, block)
		return term.Dim(coordLabel(ref.turn, ref.index, nodeCoordAt(n), layout))
	}
	return c
}

func (t *transcript) render() {
	if !t.active {
		return
	}
	// Clamp the low side before the gate: the offset is read off the frame
	// path too (viewportAnchor, the search wrap-around), and a negative index
	// panics.
	if t.offset < 0 {
		t.offset = 0
	}
	if t.batch > 0 || (t.gate != nil && !t.gate()) {
		t.dirty = true
		return
	}
	t.dirty = false
	t.renderFrame()
	if t.painted != nil {
		t.painted()
	}
}

// beginBatch/endBatch bracket state changes that must produce one frame.
func (t *transcript) beginBatch() { t.batch++ }

func (t *transcript) endBatch() {
	if t.batch > 0 {
		t.batch--
	}
	if t.batch == 0 && t.dirty {
		t.dirty = false
		t.render() // re-checks the gate, which may defer once more
	}
}

// Whoever refuses a frame owes a later flush, so the final state is always on screen.
func (t *transcript) flush() {
	if !t.active || !t.dirty || t.batch > 0 {
		return
	}
	t.dirty = false
	t.renderFrame()
	if t.painted != nil {
		t.painted()
	}
}

// The ceiling is for a stream, not for an answer: a subject switch is one discrete
// thing and appears at once. A batch still holds it.
func (t *transcript) renderNow() {
	if !t.active || t.batch > 0 {
		t.render()
		return
	}
	if t.offset < 0 {
		t.offset = 0
	}
	t.dirty = false
	t.renderFrame()
	if t.painted != nil {
		t.painted()
	}
}

func (t *transcript) renderFrame() {
	// A pane under four rows has nowhere to draw the footer and would index
	// screen[-2]: skip the frame.
	if t.h < 4 {
		return
	}
	// settle reads the index, never a full materialization of the window.
	t.settle()
	foot := t.footLines()
	body, maxOff := t.layout(len(foot))
	total := t.index.total
	if t.follow {
		t.offset = maxOff
	}
	if t.offset > maxOff {
		t.offset = maxOff
	}
	if t.offset < 0 {
		t.offset = 0
	}
	screen := t.nextScreen()
	// Only the rows about to be painted are materialized, decorated and
	// highlighted.
	t.rowBuf = t.window(t.offset, t.offset+body, t.rowBuf)
	// The row->node map is recorded from the geometry actually painted:
	// re-deriving it at click time would consult an offset a live token may
	// have moved.
	t.frameRefs = t.rowRefs(t.offset, t.offset+body, t.frameRefs[:0])
	copy(screen[:body], t.rowBuf)
	// The header covers rows: they address nothing while they cannot be seen.
	head := t.stickyLines(t.activeHighlight(), t.selectionSpan())
	copy(screen[:len(head)], head)
	for i := range min(len(head), len(t.frameRefs)) {
		t.frameRefs[i] = nodeRef{}
	}
	// Bottom-aligned: the live padding row belongs above the transcript rule,
	// not between the pit and its rule.
	if t.fullPit() {
		// Fullscreen obscures the conversation rather than dimming it.
		for r := range screen {
			if r < t.h-2 {
				screen[r] = ""
			}
		}
	}
	// The row above the rule is blank while following (layout reserved it).
	rule, bar := t.footerRows(total, body)
	// A stanza taller than the pane keeps its tail: the rows nearest the bar
	// are the ones being read.
	start := t.h - 1 - len(bar) - len(foot)
	clipped := false
	if start < 0 {
		foot = foot[-start:]
		start = 0
		clipped = true
	}
	// The closing rule carries the gutter junction (joinRule): the pit's own
	// when a panel is up, the footer's otherwise; a clipped stanza has lost
	// its rule, a fullscreen pit has no conversation under it.
	floor := t.lineCut(t.offset+body-1, t.offset+body)
	if len(foot) > 0 && !clipped && !t.fullPit() && start > 0 {
		foot[0] = joinRule(foot[0], floor, gutterCut{})
	}
	for k, l := range foot {
		if r := start + k; r >= 0 && r < t.h-len(bar) {
			screen[r] = l
		}
	}
	if t.pit.open() || t.inSearch || t.boxOpen() {
		// No closing rule and no key hints under an open pit: one rule
		// above, the rows, a blank, the bar.
		rule = ""
	}
	if top := t.h - 1 - len(bar); top >= 0 {
		if rule != "" && len(foot) == 0 && top > 0 {
			rule = joinRule(rule, floor, gutterCut{})
		}
		screen[top] = rule
		for i, row := range bar {
			if r := top + 1 + i; r >= 0 && r < t.h {
				screen[r] = row
			}
		}
	}
	t.paint(screen)
}

// Two frame buffers are swapped so a frame does not allocate a []string of screen
// height.
func (t *transcript) nextScreen() []string {
	screen := t.screenSpare
	if cap(screen) < t.h {
		screen = make([]string, t.h)
	} else {
		screen = screen[:t.h]
		clear(screen)
	}
	t.screenSpare = nil
	return screen
}

func dimTransRule(w int) string {
	if w < 3 {
		w = 3
	}
	return "\x1b[2m" + strings.Repeat("─", w) + "\x1b[0m"
}

func (t *transcript) gapRow(g *aria.Gap) string {
	label := " the rest of this turn is not loaded "
	switch n := g.Turns(); {
	case n == 1:
		label = " 1 turn not loaded "
	case n > 1:
		label = fmt.Sprintf(" %d turns not loaded ", n)
	}
	w := t.w
	if w < 3 {
		w = 3
	}
	if lw := runewidth.StringWidth(label); lw+4 > w {
		return "\x1b[2m" + clipToWidth(label, w) + "\x1b[0m"
	}
	lead := (w - runewidth.StringWidth(label)) / 2
	return "\x1b[2m" + strings.Repeat("─", lead) + label +
		strings.Repeat("─", w-lead-runewidth.StringWidth(label)) + "\x1b[0m"
}

// A tall turn is several slices, so one toggle can touch more than one.
func (t *transcript) dropTurnRows(lt int) {
	t.dropTurnsRows(map[int]struct{}{lt: {}})
}

// The cache is keyed by slice, so a per-turn call is a full scan; batch.
func (t *transcript) dropTurnsRows(lts map[int]struct{}) {
	if len(lts) == 0 {
		return
	}
	t.rowRev++
	t.openMemo = openMemo{}
	for k := range t.rowCache {
		if _, ok := lts[k.turn()]; ok {
			delete(t.rowCache, k)
		}
	}
	for k := range t.stickyCache {
		if _, ok := lts[k.turn()]; ok {
			delete(t.stickyCache, k)
		}
	}
}

type openMemo struct {
	key    sliceKey
	w      int
	blocks map[int]*memoBlock
}

type memoBlock struct {
	node   livedoc.Node
	state  ldrender.BlockState
	rows   []ldrender.Row
	out    []transcriptRow // clipped, collapsed, addressed
	reused bool
}

func (mo *openMemo) compose(block int, n livedoc.Node, state ldrender.BlockState, draw func() []ldrender.Row) []ldrender.Row {
	// A running tool is drawn against the clock: its spinner is a function of
	// the frame counter, so it is recomposed every frame.
	animating := n.Type == livedoc.NodeTool && n.Status != livedoc.StatusOK && n.Status != livedoc.StatusError
	if b, ok := mo.blocks[block]; ok {
		if !animating && b.state == state && reflect.DeepEqual(b.node, n) {
			b.reused = true
			return b.rows
		}
	}
	rows := draw()
	mo.blocks[block] = &memoBlock{node: n, state: state, rows: rows}
	return rows
}

// The clip/collapse/address pass costs as much as the composition did, so it is
// memoized too.
func (mo *openMemo) kept(block int) ([]transcriptRow, bool) {
	b, ok := mo.blocks[block]
	if !ok || !b.reused || b.out == nil {
		return nil, false
	}
	return b.out, true
}

func (mo *openMemo) keep(block int, out []transcriptRow) {
	if b, ok := mo.blocks[block]; ok {
		b.out = out
	}
}
