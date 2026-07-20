package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

func searchFixture(t *testing.T, n int) *transcript {
	t.Helper()
	ft := ldrender.NewFakeTerminal(60, 10)
	client := aria.NewClient()
	for i := 1; i <= n; i++ {
		client.Apply(aria.AriaRead{Committed: []aria.Committed{{
			LT: i, Role: "assistant",
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: fmt.Sprintf("Msg%02d body", i)}},
		}}})
	}
	tr := newTranscript(ft, 60, 10, ldrender.NodeText{}, client, "aria1234", time.Now())
	tr.enter()
	return tr
}

func TestSearch_SmartcaseAndRegex(t *testing.T) {
	if p, _ := compileSearch("msg05"); !p.match("  Msg05 body") {
		t.Fatalf("all-lowercase query must match case-insensitively")
	}
	if p, _ := compileSearch("Msg05"); p.match("  msg05 body") {
		t.Fatalf("uppercase in the query must force exact case")
	}
	p, err := compileSearch(`msg0[3-4] b.dy`)
	if err != nil {
		t.Fatal(err)
	}
	if !p.match("Msg04 body") || p.match("Msg05 body") {
		t.Fatalf("regex alternation/wildcard broken")
	}
	if p.lit != "" {
		t.Fatalf("regex query must not claim a literal for pruning")
	}
	if lp, _ := compileSearch("plain words"); lp.lit != "plain words" {
		t.Fatalf("metacharacter-free query should allow literal pruning")
	}
}

func TestSearch_RegexJumpAndNextPrev(t *testing.T) {
	tr := searchFixture(t, 9)
	tr.key('g')
	tr.key('g')
	tr.key('/')
	for _, c := range []byte(`msg0[45]`) {
		tr.key(c)
	}
	tr.key(0x0d)
	if lt := tr.lineLT[tr.offset]; lt != 4 {
		t.Fatalf("regex search should land on Msg04 (LT 4), got LT %d", lt)
	}
	tr.key('n')
	if lt := tr.lineLT[tr.offset]; lt != 5 {
		t.Fatalf("n should advance to Msg05 (LT 5), got LT %d", lt)
	}
	tr.key('N')
	if lt := tr.lineLT[tr.offset]; lt != 4 {
		t.Fatalf("N should return to Msg04 (LT 4), got LT %d", lt)
	}
	// wrap: N at the first match with no older history wraps to the last
	tr.key('N')
	if lt := tr.lineLT[tr.offset]; lt != 5 {
		t.Fatalf("N should wrap to Msg05 (LT 5), got LT %d", lt)
	}
}

func TestSearch_HighlightOverlayPreservesStyling(t *testing.T) {
	p, _ := compileSearch("body")
	styled := "\x1b[2m  Msg03 \x1b[0mbody trailer"
	got := p.highlight(styled)
	if !strings.Contains(got, "\x1b[7mbody\x1b[27m") {
		t.Fatalf("match not wrapped in reverse video: %q", got)
	}
	if !strings.HasPrefix(got, "\x1b[2m") || !strings.Contains(got, "trailer") {
		t.Fatalf("surrounding styling/text damaged: %q", got)
	}
	// A match crossing a styling boundary still highlights.
	p2, _ := compileSearch("msg03 body")
	if !p2.match(styled) {
		t.Fatalf("match must see through ANSI escapes")
	}
	if out := p2.highlight(styled); !strings.Contains(out, "\x1b[7m") {
		t.Fatalf("cross-boundary highlight missing: %q", out)
	}
	// No match → row unchanged.
	if out := p.highlight("  nothing here"); out != "  nothing here" {
		t.Fatalf("unmatched row must pass through untouched: %q", out)
	}
}

func TestSearch_FilterShowsOnlyMatches(t *testing.T) {
	tr := searchFixture(t, 9)
	tr.key('&')
	for _, c := range []byte("msg0[13]") {
		tr.key(c)
	}
	tr.key(0x0d)
	if tr.filter == nil {
		t.Fatalf("& + Enter must set the filter")
	}
	all := strings.Join(tr.lines(), "\n")
	if !strings.Contains(all, "Msg01") || !strings.Contains(all, "Msg03") {
		t.Fatalf("filter dropped matching rows:\n%s", all)
	}
	if strings.Contains(all, "Msg02") || strings.Contains(all, "Msg05") {
		t.Fatalf("filter leaked non-matching rows:\n%s", all)
	}
	// '&' + empty Enter clears.
	tr.key('&')
	tr.key(0x0d)
	if tr.filter != nil {
		t.Fatalf("empty & must clear the filter")
	}
	if all := strings.Join(tr.lines(), "\n"); !strings.Contains(all, "Msg02") {
		t.Fatalf("clearing the filter must restore the full view")
	}
}

func TestSearch_BadRegexStaysInPromptAndEscClears(t *testing.T) {
	tr := searchFixture(t, 3)
	tr.key('/')
	tr.key('[')
	tr.key(0x0d)
	if !tr.inSearch || tr.searchErr == "" {
		t.Fatalf("bad pattern must keep the prompt open with an error (inSearch=%v err=%q)", tr.inSearch, tr.searchErr)
	}
	_, status := tr.footerRows(len(tr.lines()), tr.h-2)
	if !strings.Contains(stripANSI(status), "⟨") {
		t.Fatalf("prompt row must surface the error: %q", status)
	}
	tr.key(0x1b) // Esc leaves the prompt
	if tr.inSearch {
		t.Fatalf("Esc must cancel the prompt")
	}
	// A committed search highlights until Esc (:noh).
	tr.key('/')
	for _, c := range []byte("msg02") {
		tr.key(c)
	}
	tr.key(0x0d)
	if tr.pattern == nil {
		t.Fatalf("committed search must set the pattern")
	}
	tr.key(0x1b)
	if tr.pattern != nil || tr.filter != nil {
		t.Fatalf("Esc must clear highlight and filter (:noh)")
	}
	if !tr.active {
		t.Fatalf("none of this may exit the locked pager")
	}
}

// Review findings: highlight must survive a row's own SGR reset mid-span,
// literal pruning must be ASCII-only, and the pager must re-enter clean.
func TestSearch_ReviewRegressions(t *testing.T) {
	// F7: \x1b[0m inside the match span must not cancel the highlight.
	p, _ := compileSearch("ab")
	out := p.highlight("xx a\x1b[0mb yy")
	if !strings.Contains(out, "\x1b[0m\x1b[7m") {
		t.Fatalf("reverse video not re-asserted after embedded reset: %q", out)
	}
	// F9: non-ASCII literals must not claim pruning (ToLower vs RE2 folding).
	if np, _ := compileSearch("straße"); np.lit != "" {
		t.Fatalf("non-ASCII literal must not prune")
	}
	// F6: re-entering the pager clears filter + highlight.
	tr := searchFixture(t, 5)
	tr.key('&')
	for _, c := range []byte("msg01") {
		tr.key(c)
	}
	tr.key(0x0d)
	tr.key('/')
	for _, c := range []byte("msg01") {
		tr.key(c)
	}
	tr.key(0x0d)
	tr.leave()
	tr.enter()
	if tr.filter != nil || tr.pattern != nil {
		t.Fatalf("re-entering the pager must start search-clean")
	}
}

// F3: an active '&' filter that hides one message's match must not abort the
// page scan — later matches in the same page still land.
func TestSearch_FindPageContinuesPastFilteredMessage(t *testing.T) {
	tr := searchFixture(t, 6)
	// filter keeps only Msg05/Msg06 rows; message 2 matches "body" in raw rows
	// but is invisible in the filtered view — the scan must reach Msg05.
	fp, _ := compileSearch("msg0[56]")
	tr.filter = fp
	sp, _ := compileSearch("body")
	tr.pattern = sp
	msgs := tr.client.View().Closed
	if !tr.findPage(sp, msgs, false) {
		t.Fatalf("findPage must skip filtered-out messages and land on a visible one")
	}
	if lt := tr.lineLT[tr.offset]; lt != 5 {
		t.Fatalf("expected landing on Msg05 (LT 5), got LT %d", lt)
	}
}

// F4/F5: N with no older history wraps within the window instead of arming a
// futile paged scan; n at the last match wraps to the window's first match.
func TestSearch_WindowWrapBothDirections(t *testing.T) {
	tr := searchFixture(t, 9)
	tr.findQuery("msg0[27]")
	if lt := tr.lineLT[tr.offset]; lt != 7 {
		// search starts near the tail; first forward wrap-around hit is LT 7 or 2
		// depending on start offset — normalize to the earlier match first
		tr.key('N')
	}
	tr.key('N') // from the earlier match, N wraps backward to the later one
	ltA := tr.lineLT[tr.offset]
	tr.key('n') // and n wraps forward again
	ltB := tr.lineLT[tr.offset]
	if ltA == ltB {
		t.Fatalf("n/N wrap did not alternate between the two matches (stuck at LT %d)", ltA)
	}
	if tr.search != nil {
		t.Fatalf("window wrap must not arm a paged search when no history exists")
	}
}
