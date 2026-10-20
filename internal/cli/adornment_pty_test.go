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
	"github.com/jack-work/figaro/internal/term"
)

// The form-delta adornment in a real pty, driven from a synthetic tape:
// no daemon, provider or store.

const (
	adornCols = 100
	adornRows = 40

	adornAria = "adorn01"
	adornAsk  = "how does the hall sound"
	adornCmd  = "measure"

	// Long enough to wrap: prose marks its last row, and a one-row block
	// cannot tell.
	adornProse     = "the hall sounds square, which is what a measurement says about a room whose four walls agree with one another about the length of a second"
	adornProseHead = "the hall sounds square"
	adornProseTail = "length of a second"
)

// adornKeep is a frame that never plays; the replay waits for it and stays up.
const adornKeep = 3600.0

// None of these words occurs in the prose: rows are located by their text.
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

	// A pager key promotes; Esc leaves the pager up with nothing selected.
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

// A selected block keeps its marker: with autowrap off the cursor stands on
// the last column it wrote, so an erase-to-end-of-line there clears the glyph.
func adornSelectedMarker(t *testing.T, p *adornPane) {
	p.key("Enter")
	p.key("C-p")
	rows, wash := p.rows(), p.paneWashed()
	i := p.only(t, rows, adornProseTail)
	if !wash[i] {
		t.Fatalf("the prose block is not the selection:\n%s", p.dump())
	}
	if w := ldrender.Width(rows[i]); w != p.w || !strings.HasSuffix(rows[i], deltaGlyph) {
		t.Errorf("the wash ate the marker: width %d of %d, %q\n%s", w, p.w, rows[i], p.dump())
	}
}

// Collapsed, every adorned block wears one Δ in the last column; prose on its
// last row, the others on their first.
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
	// An absence counts only with the whole aria on screen.
	p.only(t, rows, adornAsk)
	p.only(t, rows, "six")
	for _, key := range adornEveryKey() {
		if hasRow(rows, key) {
			t.Errorf("collapsed, but a delta row for %q is drawn\n%s", key, p.dump())
		}
	}
}

// Enter opens one row per delta key; the head parks at the anchor while the
// selection is on the block.
func adornInquiryOpens(t *testing.T, p *adornPane) {
	p.key("C-n")
	p.key("Enter")

	rows := p.rows()
	for _, key := range adornAskKeys {
		i := p.only(t, rows, key)
		if !strings.Contains(rows[i], "->") {
			t.Errorf("delta row %q shows no transition: %q", key, rows[i])
		}
	}
	anchor := p.onlyBare(t, rows, headGlyph)
	if first := p.only(t, rows, "address"); first != anchor+1 {
		t.Errorf("the section's first row is at %d, want %d (right under the anchor)\n%s",
			first, anchor+1, p.dump())
	}
	for _, key := range adornAskKeys {
		i := p.only(t, rows, key)
		if col := column(clean(rows[i]), 0); col != snakeBody {
			t.Errorf("delta row %q stands in column 0 on %q, want the snake's body %q",
				key, col, snakeBody)
		}
	}
	last := p.only(t, rows, adornAskKeys[len(adornAskKeys)-1])
	if !strings.HasPrefix(clean(rows[last+1]), snakeClose) {
		t.Errorf("the rule under the list does not close on %q: %q", snakeClose, clean(rows[last+1]))
	}

	wash := p.paneWashed()
	if !wash[p.only(t, rows, adornAsk)] {
		t.Errorf("the selected question is not washed\n%s", p.dump())
	}
	if !wash[anchor] {
		t.Errorf("the block's chrome row is not washed with the rest of it\n%s", p.dump())
	}
	if !strings.Contains(rows[anchor], headGlyph) {
		t.Errorf("the parked head is gone from the chrome row: %q", rows[anchor])
	}
}

// ^N and ^P walk the rows one at a time and leave onto the adjacent block.
func adornRowsWalk(t *testing.T, p *adornPane) {
	p.key("C-n")
	adornFocus(t, p, "address", "entering the list from above")
	p.key("C-n")
	adornFocus(t, p, adornAskKeys[0], "the second step")
	p.key("C-n")
	adornFocus(t, p, adornAskKeys[1], "the third step")

	p.key("C-n")
	wash := p.paneWashed()
	if !wash[p.only(t, p.rows(), adornProseHead)] {
		t.Fatalf("^N past the last row did not select the block below\n%s", p.dump())
	}
	p.key("C-p")
	adornFocus(t, p, adornAskKeys[1], "entering the list from below")

	p.key("C-p")
	adornFocus(t, p, adornAskKeys[0], "stepping up inside the list")
	p.key("C-p")
	adornFocus(t, p, "address", "the list's first row")
	p.key("C-p")
	wash, rows := p.paneWashed(), p.rows()
	if !wash[p.only(t, rows, adornAsk)] {
		t.Errorf("^P past the first row did not select the parent block\n%s", p.dump())
	}
	// The block's wash covers its delta rows wherever the head sits.
	for _, key := range adornAskKeys {
		i := p.only(t, rows, key)
		if !wash[i] {
			t.Errorf("the block's wash stopped short of its delta row %q: %q", key, clean(rows[i]))
		}
		if strings.HasPrefix(clean(rows[i]), deltaGlyph) {
			t.Errorf("the head stayed on delta row %q with the block selected: %q", key, clean(rows[i]))
		}
	}
}

// Enter on a row opens that row and keeps the list; Enter on the block closes it.
func adornCloseLands(t *testing.T, p *adornPane) {
	p.key("C-n")
	adornFocus(t, p, "address", "before closing")
	p.key("Enter")
	for _, key := range adornAskKeys {
		if !hasRow(p.rows(), key) {
			t.Errorf("Enter on a row closed the list (no row for %q)\n%s", key, p.dump())
		}
	}
	p.key("Enter")
	p.key("C-p")
	p.key("Enter")
	rows := p.rows()
	for _, key := range adornAskKeys {
		if hasRow(rows, key) {
			t.Errorf("Enter left the row for %q on screen\n%s", key, p.dump())
		}
	}
	if !p.paneWashed()[p.only(t, rows, adornAsk)] {
		t.Errorf("after closing, the question is not the selection\n%s", p.dump())
	}
}

// A tool's connector row reads Δ with the block selected and ╭ with the cursor in
// the list.
func adornToolSnake(t *testing.T, p *adornPane) {
	p.key("C-n")
	p.key("C-n")
	p.key("Enter")

	rows := p.rows()
	for _, key := range adornToolKeys {
		p.only(t, rows, key)
	}
	elbow := p.only(t, rows, snakeElbow)
	if got, want := strings.TrimRight(rows[elbow], " "), " "+headGlyph+snakeElbow; got != want {
		t.Errorf("the tool's connector reads %q, want %q (the head parks at the anchor)", got, want)
	}
	if first := p.only(t, rows, "address"); first != elbow+1 {
		t.Errorf("the tool section's first row is at %d, want %d", first, elbow+1)
	}

	p.key("C-n")
	rows = p.rows()
	elbow = p.only(t, rows, snakeElbow)
	if got, want := strings.TrimRight(rows[elbow], " "), " "+snakeHang+snakeElbow; got != want {
		t.Errorf("with the cursor in the list the connector reads %q, want %q", got, want)
	}
	adornFocus(t, p, "address", "the tool's first row")

	rows = p.rows()
	i := p.only(t, rows, "address")
	if !p.paneWashed()[i] || !strings.Contains(rows[i], headGlyph) {
		t.Errorf("the focused row lost its head or its wash: %q", rows[i])
	}

	p.key("C-p")
	p.key("Enter")
}

// e opens only the tool's body; Enter opens body and list.
func adornBodyAndList(t *testing.T, p *adornPane) {
	p.key("e")
	rows := p.rows()
	if !hasRow(rows, "bash") {
		t.Fatalf("e did not open the tool's body\n%s", p.dump())
	}
	for _, key := range adornToolKeys {
		if hasRow(rows, key) {
			t.Errorf("e opened the delta list too (row for %q)\n%s", key, p.dump())
		}
	}

	p.key("e")
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

// adornFocus: the head says which row, the wash says which block.
func adornFocus(t *testing.T, p *adornPane, key, what string) {
	t.Helper()
	wash, plain := p.paneWashed(), p.rows()
	i := p.only(t, plain, key)
	if !wash[i] {
		t.Errorf("%s: the row for %q is not washed with its block\n%s", what, key, p.dump())
		return
	}
	head := deltaGlyph
	if key == "address" {
		head = headGlyph
	}
	if !strings.HasPrefix(clean(plain[i]), head) && !strings.Contains(plain[i], " "+head+" ") {
		t.Errorf("%s: the row for %q carries no head: %q", what, key, plain[i])
	}
	for j, row := range plain {
		if c := clean(row); j != i && (strings.HasPrefix(c, headGlyph+" ") || strings.HasPrefix(c, deltaGlyph+" ") || strings.HasPrefix(c, " "+headGlyph+" ") || strings.HasPrefix(c, " "+deltaGlyph+" ")) {
			t.Errorf("%s: row %d carries a second head: %q", what, j, row)
		}
	}
}

// adornPane replays the fixture tape on a private tmux server, never the user's.
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

	// A worktree's .git is a file, so `go build` records no revision; stamp it.
	bin := os.Getenv("FIGARO_E2E_BIN")
	if bin == "" {
		bin = filepath.Join(dir, "figaro")
		rev, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil {
			t.Skipf("git rev-parse: %v", err)
		}
		build := exec.Command("go", "build", "-ldflags",
			"-X github.com/jack-work/figaro/internal/boot.commit="+strings.TrimSpace(string(rev)),
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
	var panePID int
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", p.sock, "kill-server").Run()
		awaitPaneGone(t, panePID)
	})

	cmd := fmt.Sprintf("env FIGARO_STATE_DIR=%s/state FIGARO_RUNTIME_DIR=%s/run FIGARO_CONFIG_DIR=%s/config %s replay %s",
		dir, dir, dir, bin, tapePath)
	out, err := exec.Command("tmux", "-S", p.sock, "-f", conf, "new-session", "-d",
		"-x", fmt.Sprint(p.w), "-y", fmt.Sprint(p.h), "sh", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Skipf("tmux new-session: %v: %s", err, out)
	}
	fmt.Sscan(strings.TrimSpace(p.tmux("display", "-p", "-t", "0", "#{pane_pid}")), &panePID)
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
	cmd.Env = append(os.Environ(), "TMUX=")
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

// key sends one keystroke and settles; a whole string arrives as a single read.
func (p *adornPane) key(k string) {
	p.t.Helper()
	if len(k) == 1 {
		p.tmux("send-keys", "-t", "0", "-l", k)
	} else {
		p.tmux("send-keys", "-t", "0", k)
	}
	p.settle()
}

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

func (p *adornPane) rows() []string   { return splitPane(p.capture(false)) }
func (p *adornPane) styled() []string { return splitPane(p.last) }

func splitPane(capture string) []string {
	return strings.Split(strings.TrimRight(capture, "\n"), "\n")
}

// only fails on none or several rows: an ambiguous row asserts whichever came first.
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

// The collapsed marker and the parked head are the same glyph; a bare row names
// the chrome row.
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

// Read once with colour forced: a test process is not a terminal, and an
// empty wash would match every row.
var nodeWashes = func() []string {
	defer term.SetColorMode(term.ColorAlways)()
	return []string{term.NodeWash(false), term.NodeWash(true)}
}()

// washed is for rows the painter produced, which carry renditions from column zero.
func washed(row string) bool {
	return strings.Contains(row, nodeWashes[0]) || strings.Contains(row, nodeWashes[1])
}

// `capture-pane -e` emits an escape only where attributes change and carries
// them across lines, so a row read alone can look unwashed: track the
// background in force from the top of the capture.
func (p *adornPane) paneWashed() []bool { return capturedWashes(p.last) }

func capturedWashes(capture string) []bool {
	var out []bool
	bg := ""
	for _, line := range splitPane(capture) {
		at, seen := bg, false
		for i := 0; i < len(line); {
			if line[i] != 0x1b {
				if !seen {
					at, seen = bg, true
				}
				i++
				continue
			}
			j := i + 1
			for j < len(line) && !(line[j] >= 'A' && line[j] <= 'Z' || line[j] >= 'a' && line[j] <= 'z') {
				j++
			}
			if j < len(line) {
				j++
			}
			bg = applyBackground(bg, line[i:j])
			i = j
		}
		if !seen {
			at = bg
		}
		out = append(out, at == nodeWashes[0] || at == nodeWashes[1])
	}
	return out
}

// applyBackground spells the background the way the palette does.
func applyBackground(bg, esc string) string {
	if !strings.HasSuffix(esc, "m") {
		return bg
	}
	params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(esc, "\x1b["), "m"), ";")
	for i := 0; i < len(params); i++ {
		switch params[i] {
		case "", "0", "49":
			bg = ""
		case "48":
			if i+2 < len(params) && params[i+1] == "5" {
				bg = "\x1b[48;5;" + params[i+2] + "m"
				i += 2
			} else if i+4 < len(params) && params[i+1] == "2" {
				bg = "\x1b[48;2;" + strings.Join(params[i+2:i+5], ";") + "m"
				i += 4
			}
		}
	}
	return bg
}

func clean(row string) string { return visibleText(row) }

func column(row string, col int) string {
	rs := []rune(row)
	if col >= len(rs) {
		return ""
	}
	return string(rs[col])
}

func adornDelta(form, kind, value, prev string) livedoc.FormDelta {
	d := livedoc.FormDelta{Kind: livedoc.FormKind(kind), Event: livedoc.FormSet, Form: form}
	d.Value, _ = json.Marshal(value)
	if prev != "" {
		d.Prev, _ = json.Marshal(prev)
	}
	return d
}

// adornPage: one sealed turn whose question, prose and tool each carry two deltas.
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

// The frame offsets are the fixture: the page lands at once and the last
// frame is an hour out.
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
