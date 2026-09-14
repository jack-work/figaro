package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/tape"
)

// THE ADORNMENT, IN A REAL TERMINAL.
//
// Everything else that tests the form-delta adornment tests the pager's
// model: what it decided to paint. The adornment is a paint. Its glyphs sit
// in the last column, in the margin the selection bar wants, and under a
// wash that re-emits itself after every reset the row carries, so the only
// instrument that can settle a claim about them is a terminal.
//
// The binary is driven from a synthetic tape (`figaro replay`), which means
// no daemon, no provider and no store: the aria is a fixture, and the same
// keystrokes paint the same screen every run.

const (
	adornCols = 100
	adornRows = 40

	adornAria = "adorn01"
	adornAsk  = "how does the hall sound"
	adornCmd  = "measure"

	// The prose is long enough to WRAP, because prose is the one layout
	// that tacks its marker onto its last row rather than its first, and a
	// one-row block cannot tell the two apart.
	adornProse     = "the hall sounds square, which is what a measurement says about a room whose four walls agree with one another about the length of a second"
	adornProseHead = "the hall sounds square"
	adornProseTail = "length of a second"
)

// adornKeep is the offset of a frame that never plays. The replay's pusher
// waits for it, which is what keeps the session up and taking keys; without
// it the tape runs out and the renderer leaves through its end-of-stream
// door mid-test.
const adornKeep = 3600.0

// The keys each block's deltas carry. They are distinct words, and none of
// them occurs in the aria's prose: a row is located on screen by its text.
var (
	adornAskKeys   = []string{"mantra", "mode"}
	adornProseKeys = []string{"gain", "tone"}
	adornToolKeys  = []string{"damping", "decay"}
)

func adornEveryKey() []string {
	out := make([]string, 0, len(adornAskKeys)+len(adornProseKeys)+len(adornToolKeys))
	out = append(out, adornAskKeys...)
	out = append(out, adornProseKeys...)
	return append(out, adornToolKeys...)
}

func TestAdornmentPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("drives the real binary through tmux; skipped under -short")
	}
	p := newAdornPane(t)

	// The pager owns the selection, and a pager key is what promotes to it.
	// Esc then leaves it up with nothing selected, which is the state the
	// collapsed claim is about.
	p.key("C-n")
	p.key("Escape")

	t.Run("collapsed-gutter", func(t *testing.T) { adornCollapsedGutter(t, p) })
	t.Run("inquiry-list-opens", func(t *testing.T) { adornInquiryOpens(t, p) })
	t.Run("rows-walk-one-at-a-time", func(t *testing.T) { adornRowsWalk(t, p) })
	t.Run("d-closes-onto-the-block", func(t *testing.T) { adornCloseLands(t, p) })
	t.Run("tool-snake", func(t *testing.T) { adornToolSnake(t, p) })
	t.Run("t-opens-the-body-only", func(t *testing.T) { adornBodyAndList(t, p) })
	t.Run("selected-block-keeps-its-marker", func(t *testing.T) { adornSelectedMarker(t, p) })
}

// A SELECTED BLOCK STILL WEARS ITS MARKER. This is the claim a terminal model
// cannot make: the wash used to close with an erase-to-end-of-line, and with
// autowrap off the cursor still stands on the last column it wrote, so the
// erase cleared the glyph it had just put there. Only a real pane shows it.
func adornSelectedMarker(t *testing.T, p *adornPane) {
	// Back to the collapsed state, with the prose block selected: its marker
	// rides its last row, which is the row the selection washes. The tool is
	// the last block, so prose is one step BACK from it.
	p.key("Enter") // close what the previous case opened
	p.key("C-p")
	rows, styled := p.rows(), p.styled()
	i := p.only(t, rows, adornProseTail)
	if !washed(styled[i]) {
		t.Fatalf("the prose block is not the selection:\n%s", p.dump())
	}
	if w := ldrender.Width(rows[i]); w != p.w || !strings.HasSuffix(rows[i], deltaGlyph) {
		t.Errorf("the wash ate the marker: width %d of %d, %q\n%s", w, p.w, rows[i], p.dump())
	}
}

// Collapsed, every adorned block wears one Δ and it stands in the LAST
// column: a block that carries state says so in one column until it is
// asked. Prose wears it on its last row, the other two on their first.
func adornCollapsedGutter(t *testing.T, p *adornPane) {
	rows := p.rows()
	for _, want := range []string{"> input", adornProseTail, "$ " + adornCmd} {
		i := p.only(t, rows, want)
		if w := ldrender.Width(rows[i]); w != p.w || !strings.HasSuffix(rows[i], deltaGlyph) {
			t.Errorf("row %d (%q) does not wear %s in the last column: width %d of %d, %q",
				i, want, deltaGlyph, w, p.w, rows[i])
		}
	}
	if got, want := countGlyph(rows, deltaGlyph), 3; got != want {
		t.Errorf("%d rows carry %s, want %d (one per adorned block)\n%s",
			got, deltaGlyph, want, p.dump())
	}
	if head := p.only(t, rows, adornProseHead); strings.HasSuffix(rows[head], deltaGlyph) {
		t.Errorf("prose wears its marker on row %d, its FIRST: %q", head, rows[head])
	}
	// An absence is only an absence when the whole aria is on screen: the
	// question and the tool's last output line are both in the capture, so
	// nothing is hiding above or below the window.
	p.only(t, rows, adornAsk)
	p.only(t, rows, "six")
	for _, key := range adornEveryKey() {
		if hasRow(rows, key) {
			t.Errorf("collapsed, but a delta row for %q is drawn\n%s", key, p.dump())
		}
	}
}

// Enter on a selected block opens one row per delta key, and the snake
// appears: the head parks at the anchor while the selection is on the block
// itself.
func adornInquiryOpens(t *testing.T, p *adornPane) {
	p.key("C-n") // the question is the first block
	p.key("Enter")

	rows := p.rows()
	for _, key := range adornAskKeys {
		i := p.only(t, rows, key)
		if !strings.Contains(rows[i], "->") {
			t.Errorf("delta row %q shows no transition: %q", key, rows[i])
		}
	}
	// The anchor is the blank row between the question and its rows, and it
	// holds the parked head.
	anchor := p.onlyBare(t, rows, deltaGlyph)
	if first := p.only(t, rows, adornAskKeys[0]); first != anchor+1 {
		t.Errorf("the first delta row is at %d, want %d (right under the anchor)\n%s",
			first, anchor+1, p.dump())
	}
	for _, key := range adornAskKeys {
		i := p.only(t, rows, key)
		if col := column(clean(rows[i]), 0); col != snakeBody {
			t.Errorf("delta row %q stands in column 0 on %q, want the snake's body %q",
				key, col, snakeBody)
		}
	}
	// The question ENCLOSES its deltas: the rule that closes the seam is the
	// corner the snake runs down to.
	last := p.only(t, rows, adornAskKeys[len(adornAskKeys)-1])
	if !strings.HasPrefix(clean(rows[last+1]), snakeClose) {
		t.Errorf("the rule under the list does not close on %q: %q", snakeClose, clean(rows[last+1]))
	}

	// The wash. The question's own text is selected and washed; the
	// adornment's chrome row is not, and the head stays visible on it.
	styled := p.styled()
	if !washed(styled[p.only(t, styled, adornAsk)]) {
		t.Errorf("the selected question is not washed\n%s", p.dump())
	}
	if washed(styled[anchor]) {
		t.Errorf("the selection wash swallowed the adornment's chrome row: %q", clean(styled[anchor]))
	}
	if !strings.Contains(clean(styled[anchor]), deltaGlyph) {
		t.Errorf("the parked head is gone from the chrome row: %q", clean(styled[anchor]))
	}
}

// ^N and ^P walk the rows ONE AT A TIME. Entering from above lands on the
// first row, from below on the last, and stepping up past the first row
// selects the block the rows explain.
func adornRowsWalk(t *testing.T, p *adornPane) {
	p.key("C-n")
	adornFocus(t, p, adornAskKeys[0], "entering the list from above")
	p.key("C-n")
	adornFocus(t, p, adornAskKeys[1], "the second step")

	// Out of the list, onto the block below it, then back in from below.
	p.key("C-n")
	styled := p.styled()
	if !washed(styled[p.only(t, styled, adornProseHead)]) {
		t.Fatalf("^N past the last row did not select the block below\n%s", p.dump())
	}
	p.key("C-p")
	adornFocus(t, p, adornAskKeys[1], "entering the list from below")

	// Up through the rows and out of the top, onto the parent.
	p.key("C-p")
	adornFocus(t, p, adornAskKeys[0], "stepping up inside the list")
	p.key("C-p")
	styled = p.styled()
	if !washed(styled[p.only(t, styled, adornAsk)]) {
		t.Errorf("^P past the first row did not select the parent block\n%s", p.dump())
	}
	for _, key := range adornAskKeys {
		if row := styled[p.only(t, styled, key)]; washed(row) {
			t.Errorf("the parent's wash reached delta row %q: %q", key, clean(row))
		}
	}
}

// Enter again closes the list, and the selection lands back on the block: the
// gesture acts on the block wherever the cursor stands inside its list.
func adornCloseLands(t *testing.T, p *adornPane) {
	p.key("C-n") // into the list, so the close has somewhere to land FROM
	adornFocus(t, p, adornAskKeys[0], "before closing")
	p.key("Enter")
	rows := p.rows()
	for _, key := range adornAskKeys {
		if hasRow(rows, key) {
			t.Errorf("Enter left the row for %q on screen\n%s", key, p.dump())
		}
	}
	styled := p.styled()
	if !washed(styled[p.only(t, styled, adornAsk)]) {
		t.Errorf("after closing, the question is not the selection\n%s", p.dump())
	}
}

// A tool hangs its snake off a connector row under its output: Δ at the
// anchor while the selection is on the block, ╭ once the cursor is in the
// list, and the elbow turns out of the gutter the tool widget draws.
func adornToolSnake(t *testing.T, p *adornPane) {
	p.key("C-n") // prose
	p.key("C-n") // tool
	p.key("Enter")

	rows := p.rows()
	for _, key := range adornToolKeys {
		p.only(t, rows, key)
	}
	elbow := p.only(t, rows, snakeElbow)
	if got, want := strings.TrimRight(rows[elbow], " "), " "+deltaGlyph+snakeElbow; got != want {
		t.Errorf("the tool's connector reads %q, want %q (the head parks at the anchor)", got, want)
	}
	if first := p.only(t, rows, adornToolKeys[0]); first != elbow+1 {
		t.Errorf("the first tool delta row is at %d, want %d", first, elbow+1)
	}

	p.key("C-n")
	rows = p.rows()
	elbow = p.only(t, rows, snakeElbow)
	if got, want := strings.TrimRight(rows[elbow], " "), " "+snakeHang+snakeElbow; got != want {
		t.Errorf("with the cursor in the list the connector reads %q, want %q", got, want)
	}
	adornFocus(t, p, adornToolKeys[0], "the tool's first row")

	// The head is drawn INTO the washed row, and the wash does not eat it.
	styled := p.styled()
	row := styled[p.only(t, styled, adornToolKeys[0])]
	if !washed(row) || !strings.Contains(row, deltaGlyph) {
		t.Errorf("the focused row lost its head or its wash: %q", clean(row))
	}

	p.key("C-p")   // back to the block
	p.key("Enter") // and closed
}

// t is the tool's body and NOTHING else; Enter is both halves at once.
func adornBodyAndList(t *testing.T, p *adornPane) {
	p.key("t")
	rows := p.rows()
	if !hasRow(rows, "bash") {
		t.Fatalf("t did not open the tool's body\n%s", p.dump())
	}
	for _, key := range adornToolKeys {
		if hasRow(rows, key) {
			t.Errorf("t opened the delta list too (row for %q)\n%s", key, p.dump())
		}
	}

	p.key("t") // closed again, so Enter opens both from the same place
	p.key("Enter")
	rows = p.rows()
	if !hasRow(rows, "bash") {
		t.Errorf("Enter did not open the tool's body\n%s", p.dump())
	}
	for _, key := range adornToolKeys {
		if !hasRow(rows, key) {
			t.Errorf("Enter did not open the delta list (no row for %q)\n%s", key, p.dump())
		}
	}
}

// adornFocus asserts the cursor stands on the row holding key: washed, with
// the snake's head in its spine column, and alone in both.
func adornFocus(t *testing.T, p *adornPane, key, what string) {
	t.Helper()
	styled, plain := p.styled(), p.rows()
	i := p.only(t, styled, key)
	if !washed(styled[i]) {
		t.Errorf("%s: the row for %q is not the focus\n%s", what, key, p.dump())
		return
	}
	if !strings.Contains(plain[i], deltaGlyph) {
		t.Errorf("%s: the row for %q carries no head: %q", what, key, plain[i])
	}
	for j, row := range styled {
		if j != i && washed(row) && strings.Contains(clean(row), "->") {
			t.Errorf("%s: row %d is a washed delta row too: %q", what, j, clean(row))
		}
	}
}

// ------------------------------------------------------------------ pane ---

// adornPane is a figaro replaying the fixture tape in a pty of its own, on a
// PRIVATE tmux server: its own socket, its own config, killed on the way out.
// A test that reaches the user's tmux server is a test that can lose his
// session to a cleanup.
type adornPane struct {
	t    *testing.T
	sock string
	w, h int
	last string
}

func newAdornPane(t *testing.T) *adornPane {
	t.Helper()
	return newAdornPaneFor(t, adornPage(), adornProseHead)
}

func newAdornPaneFor(t *testing.T, page aria.Page, ready string) *adornPane {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	dir := t.TempDir()
	tapePath := filepath.Join(dir, "adorn.tape")
	writeAdornPageTape(t, tapePath, page)

	// A plain `go build` in a worktree records no revision (a worktree's
	// .git is a file, and Go's VCS autodetection wants a directory), so the
	// build handshake has nothing to compare. Stamp it.
	bin := os.Getenv("FIGARO_E2E_BIN")
	if bin == "" {
		bin = filepath.Join(dir, "figaro")
		rev, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil {
			t.Skipf("git rev-parse: %v", err)
		}
		build := exec.Command("go", "build", "-ldflags",
			"-X github.com/jack-work/figaro/internal/cli.commit="+strings.TrimSpace(string(rev)),
			"-o", bin, "../../cmd/figaro")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build figaro: %v: %s", err, out)
		}
	}

	conf := filepath.Join(dir, "tmux.conf")
	if err := os.WriteFile(conf, []byte("set -g default-terminal \"screen-256color\"\nset -g status off\nset -g remain-on-exit on\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"state", "run", "config"} {
		if err := os.MkdirAll(filepath.Join(dir, s), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	p := &adornPane{t: t, sock: filepath.Join(dir, "tmux.sock"), w: adornCols, h: adornRows}
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", p.sock, "kill-server").Run() })

	// -y h+1: tmux gives back h, because the status bar takes a row whether
	// or not it is drawn. The height is read back below rather than assumed.
	cmd := fmt.Sprintf("env FIGARO_STATE_DIR=%s/state FIGARO_RUNTIME_DIR=%s/run FIGARO_CONFIG_DIR=%s/config %s replay %s",
		dir, dir, dir, bin, tapePath)
	out, err := exec.Command("tmux", "-S", p.sock, "-f", conf, "new-session", "-d",
		"-x", fmt.Sprint(p.w), "-y", fmt.Sprint(p.h+1), "sh", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Skipf("tmux new-session: %v: %s", err, out)
	}
	if got := p.height(); got != p.h {
		p.h = got
	}
	p.settle()
	if !hasRow(p.rows(), ready) {
		t.Fatalf("the fixture aria never painted\n%s", p.dump())
	}
	return p
}

func (p *adornPane) tmux(args ...string) string {
	p.t.Helper()
	cmd := exec.Command("tmux", append([]string{"-S", p.sock}, args...)...)
	cmd.Env = append(os.Environ(), "TMUX=") // never speak to an outer session
	out, err := cmd.CombinedOutput()
	if err != nil {
		p.t.Fatalf("tmux %v: %v: %s", args, err, out)
	}
	return string(out)
}

func (p *adornPane) height() int {
	var h int
	fmt.Sscan(strings.TrimSpace(p.tmux("display", "-p", "-t", "0", "#{pane_height}")), &h)
	return h
}

// key sends one keystroke and waits for the pane to stop moving. One key per
// call: a whole string arrives as a single read, which is the one input
// pattern no reader ever produces.
func (p *adornPane) key(k string) {
	p.t.Helper()
	if len(k) == 1 {
		p.tmux("send-keys", "-t", "0", "-l", k)
	} else {
		p.tmux("send-keys", "-t", "0", k)
	}
	p.settle()
}

// settle polls until two captures agree. A fixed sleep is a guess about a
// repaint, and the wrong guess reads as a bug that is not there.
func (p *adornPane) settle() {
	p.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	prev, stable := "", 0
	for time.Now().Before(deadline) {
		time.Sleep(80 * time.Millisecond)
		cur := p.capture(true)
		if cur == prev && strings.TrimSpace(cur) != "" {
			if stable++; stable == 2 {
				p.last = cur
				return
			}
		} else {
			stable = 0
		}
		prev = cur
	}
	p.last = prev
	p.t.Fatalf("the pane never stopped repainting\n%s", p.dump())
}

func (p *adornPane) capture(escapes bool) string {
	args := []string{"capture-pane", "-p", "-t", "0"}
	if escapes {
		args = append(args, "-e")
	}
	return p.tmux(args...)
}

// rows is the pane as a reader sees it; styled is the same pane with the
// renditions left in, which is the only capture a colour claim may rest on.
func (p *adornPane) rows() []string   { return splitPane(p.capture(false)) }
func (p *adornPane) styled() []string { return splitPane(p.last) }

func splitPane(capture string) []string {
	return strings.Split(strings.TrimRight(capture, "\n"), "\n")
}

// only is the index of the one row holding want, and a fatal error when the
// screen holds none or several: an assertion aimed at an ambiguous row is an
// assertion about whichever row the layout happens to put first.
func (p *adornPane) only(t *testing.T, rows []string, want string) int {
	t.Helper()
	found := -1
	for i, r := range rows {
		if strings.Contains(clean(r), want) {
			if found >= 0 {
				t.Fatalf("%q is on rows %d and %d\n%s", want, found, i, p.dump())
			}
			found = i
		}
	}
	if found < 0 {
		t.Fatalf("no row holds %q\n%s", want, p.dump())
	}
	return found
}

func (p *adornPane) dump() string {
	return "--- pane ---\n" + p.capture(false) + "------------"
}

// onlyBare is the index of the one row whose whole content is want. The
// collapsed marker and the parked head are the same glyph, so a row holding
// nothing else is how the chrome row is named.
func (p *adornPane) onlyBare(t *testing.T, rows []string, want string) int {
	t.Helper()
	found := -1
	for i, r := range rows {
		if strings.TrimSpace(clean(r)) != want {
			continue
		}
		if found >= 0 {
			t.Fatalf("%q is the whole of rows %d and %d\n%s", want, found, i, p.dump())
		}
		found = i
	}
	if found < 0 {
		t.Fatalf("no row holds %q and nothing else\n%s", want, p.dump())
	}
	return found
}

func hasRow(rows []string, want string) bool {
	for _, r := range rows {
		if strings.Contains(clean(r), want) {
			return true
		}
	}
	return false
}

func countGlyph(rows []string, glyph string) int {
	n := 0
	for _, r := range rows {
		n += strings.Count(clean(r), glyph)
	}
	return n
}

// washed reports the selection lift, by the very constant the pager paints.
func washed(row string) bool { return strings.Contains(row, selBg) }

// clean is a captured row with its renditions dropped.
func clean(row string) string { return visibleText(row) }

// column is the display column col of a plain row.
func column(row string, col int) string {
	rs := []rune(row)
	if col >= len(rs) {
		return ""
	}
	return string(rs[col])
}

// ----------------------------------------------------------------- fixture ---

func adornDelta(form, kind, value, prev string) livedoc.FormDelta {
	d := livedoc.FormDelta{Kind: livedoc.FormKind(kind), Event: livedoc.FormSet, Form: form}
	d.Value, _ = json.Marshal(value)
	if prev != "" {
		d.Prev, _ = json.Marshal(prev)
	}
	return d
}

// adornPage is the fixture: one sealed turn whose question, whose prose and
// whose tool each carry two form deltas. Three blocks, three layouts.
func adornPage() aria.Page {
	turn := aria.Turn{
		ID: 1, Sealed: true, Inquiry: adornAsk,
		FormDeltas: map[string]livedoc.FormDelta{
			"@board.mantra": adornDelta("@board", "bound", "listening", "idle"),
			"@board.mode":   adornDelta("@board", "bound", "concise", ""),
		},
		Nodes: []livedoc.Node{
			{
				Type: livedoc.NodeProse, Markdown: adornProse,
				FormDeltas: map[string]livedoc.FormDelta{
					"@board.tone": adornDelta("@board", "bound", "dry", "wet"),
					"@board.gain": adornDelta("@board", "bound", "11", "10"),
				},
			},
			{
				Type: livedoc.NodeTool, Name: "bash", Status: "ok",
				Args: map[string]any{"command": adornCmd}, Summary: adornCmd,
				Output: "one\ntwo\nthree\nfour\nfive\nsix",
				FormDeltas: map[string]livedoc.FormDelta{
					"@board.decay":   adornDelta("@board", "bound", "0.4s", "0.9s"),
					"@board.damping": adornDelta("@board", "bound", "high", "low"),
				},
			},
		},
	}
	return aria.Page{Parts: []aria.TurnPart{{Turn: turn}}}
}

// writeAdornTape hand-writes the tape instead of recording one, because the
// frame OFFSETS are the fixture: the page lands at once, and the last frame
// is an hour out so the replay stays on screen for as long as a test drives
// it.
func writeAdornTape(t *testing.T, path string) {
	t.Helper()
	writeAdornPageTape(t, path, adornPage())
}

func writeAdornPageTape(t *testing.T, path string, page aria.Page) {
	t.Helper()
	line := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + "\n"
	}
	msg := func(p aria.Page) json.RawMessage {
		b, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": rpc.MethodAriaFrame, "params": p,
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var b strings.Builder
	b.WriteString(line(tape.Header{
		Tape: tape.FormatVersion, Aria: adornAria,
		Started: time.Now().Format(time.RFC3339Nano),
		Cols:    adornCols, Rows: adornRows, Term: "screen-256color",
		Note: "synthetic: one turn whose question, prose and tool all carry form deltas",
	}))
	b.WriteString(line(tape.Frame{T: 0.05, Dir: tape.In, Msg: msg(page)}))
	b.WriteString(line(tape.Frame{T: adornKeep, Dir: tape.In, Msg: msg(page)}))
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}
