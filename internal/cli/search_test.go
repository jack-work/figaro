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
