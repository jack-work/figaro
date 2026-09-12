package figaro

import (
	"context"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// quoteLog builds a log with three messages so every refusal has a target:
//
//	lt 1  input   "the question"
//	lt 2  output  [prose "Alpha bravo charlie delta echo." , tool_invoke bash]
//	lt 3  input   [tool_result "ran fine"]
//	lt 4  output  [thinking "hmm", prose "Foxtrot golf hotel."]
func quoteLog(t *testing.T) *store.MemLog[message.Message] {
	t.Helper()
	log := store.NewMemLog[message.Message]()
	add := func(m message.Message) {
		if _, err := log.Append(store.Entry[message.Message]{Payload: m}); err != nil {
			t.Fatal(err)
		}
	}
	add(message.Message{Role: message.RoleInput, TurnID: 1, Content: []message.Content{message.TextContent("the question")}})
	add(message.Message{Role: message.RoleOutput, TurnID: 1, Content: []message.Content{
		message.TextContent("Alpha bravo charlie delta echo."),
		{Type: message.ContentToolInvoke, ToolCallID: "t1", ToolName: "bash", Arguments: map[string]any{"command": "true"}},
	}})
	add(message.Message{Role: message.RoleInput, TurnID: 1, Content: []message.Content{
		message.ToolResultContent("t1", "bash", "ran fine", false),
	}})
	add(message.Message{Role: message.RoleOutput, TurnID: 1, Content: []message.Content{
		{Type: message.ContentThinking, Text: "hmm"},
		message.TextContent("Foxtrot golf hotel."),
	}})
	return log
}

func mustRange(t *testing.T, s string) quote.Range {
	t.Helper()
	r, _, err := quote.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResolveQuote(t *testing.T) {
	log := quoteLog(t)
	cases := []struct{ tok, want string }{
		{"<2.0:0-5>!", "Alpha"},
		{"<2.0:6-13>!", "bravo c"},
		{"<2.0:0-31>!", "Alpha bravo charlie delta echo."},
		{"<2.0>!", "Alpha bravo charlie delta echo."},
		{"<2>!", "Alpha bravo charlie delta echo."},
		{"<4>!", "hmm\n\nFoxtrot golf hotel."},
		{"<3.0>!", "ran fine"},
		{"<4.0:1-4.1:7>!", "mm\n\nFoxtrot"},
		{"<2.0:26-4.1:7>!", "echo.\n\nran fine\n\nhmm\n\nFoxtrot"},
	}
	for _, c := range cases {
		got, err := resolveQuote(log, mustRange(t, c.tok))
		if err != nil {
			t.Fatalf("%s: %v", c.tok, err)
		}
		if got != c.want {
			t.Fatalf("%s: got %q, want %q", c.tok, got, c.want)
		}
	}
}

// Every refusal is one sentence that names what the log holds instead.
func TestResolveQuoteRefuses(t *testing.T) {
	log := quoteLog(t)
	cases := []struct{ tok, want string }{
		{"<9>!", "no message at lt 9"},
		{"<2.5>!", "lt 2 has 2 blocks, not block 5"},
		{"<1.1>!", "lt 1 has 1 block, not block 1"},
		{"<2.1>!", "block 1 of lt 2 is a tool call and has no text"},
		{"<2.0:23-9000>!", "block 0 of lt 2 has 31 chars, not 23-9000"},
		{"<2.0:40-41>!", "block 0 of lt 2 has 31 chars, not 40-41"},
		{"<2.0:0-3.9:1>!", "lt 3 has 1 block, not block 9"},
		{"<2.1:0-4.0:1>!", "block 1 of lt 2 is a tool call"},
	}
	for _, c := range cases {
		_, err := resolveQuote(log, mustRange(t, c.tok))
		if err == nil {
			t.Fatalf("%s: resolved, want a refusal mentioning %q", c.tok, c.want)
		}
		if err.Error() != c.want && !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: %q, want %q", c.tok, err, c.want)
		}
	}
}

// Rune offsets, not bytes: a coordinate can never land inside a character.
func TestResolveQuoteCountsRunes(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	_, _ = log.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleOutput, Content: []message.Content{message.TextContent("héllo wörld 日本")}}})
	got, err := resolveQuote(log, mustRange(t, "<1.0:6-14>!"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "wörld 日本" {
		t.Fatalf("got %q", got)
	}
	if _, err := resolveQuote(log, mustRange(t, "<1.0:0-15>!")); err == nil || !strings.Contains(err.Error(), "has 14 chars") {
		t.Fatalf("byte length leaked into the count: %v", err)
	}
}

func quoteSettings(head, tail int) *config.Loaded {
	l := &config.Loaded{}
	l.Config.Quote.HeadChars = &head
	l.Config.Quote.TailChars = &tail
	return l
}

func TestRenderQuoteTruncates(t *testing.T) {
	log := quoteLog(t)
	view := InputView{AriaID: "abcd1234", Log: log, Settings: quoteSettings(5, 3)}
	r := mustRange(t, "<2.0>!")
	got := renderQuote(view, r, "Alpha bravo charlie delta echo.")
	want := "> quoting aria abcd1234 · turn 1 · lt 2.0 (31 chars)\n> Alpha…ho.\n\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// A passage no longer than head+tail is sent whole. Truncating 8 chars into
// 8 chars plus an ellipsis is a bug that reads as a feature.
func TestRenderQuoteShortIsWhole(t *testing.T) {
	log := quoteLog(t)
	view := InputView{Log: log, Settings: quoteSettings(5, 3)}
	got := renderQuote(view, mustRange(t, "<3.0>!"), "ran fine")
	if strings.Contains(got, "…") {
		t.Fatalf("an 8-char passage under head+tail=8 was ellipsized: %q", got)
	}
	if !strings.Contains(got, "> ran fine\n") {
		t.Fatalf("passage missing: %q", got)
	}
}

func TestRenderQuoteHonoursGutterAndHeader(t *testing.T) {
	l := quoteSettings(480, 160)
	g, h := "| ", false
	l.Config.Quote.Gutter, l.Config.Quote.Header = &g, &h
	view := InputView{Log: quoteLog(t), Settings: l}
	got := renderQuote(view, mustRange(t, "<4>!"), "hmm\n\nFoxtrot golf hotel.")
	if got != "| hmm\n| \n| Foxtrot golf hotel.\n\n" {
		t.Fatalf("got %q", got)
	}
}

func TestFormRefsExpandAndRefuse(t *testing.T) {
	view := InputView{Form: snapshotOf(map[string]any{"mantra": "fix the bug", "n": 3, "cwd": "/tmp"})}
	var rw formRefs
	got, err := rw.Rewrite(context.Background(), view, "@mantra! in @cwd! x@n! mail me@example.com @nope")
	if err != nil {
		t.Fatal(err)
	}
	if got != "fix the bug in /tmp x3 mail me@example.com @nope" {
		t.Fatalf("got %q", got)
	}
	_, err = rw.Rewrite(context.Background(), view, "see @missing! please")
	if err == nil || err.Error() != "@missing! is not on the board" {
		t.Fatalf("an unknown terminated key must refuse and name itself: %v", err)
	}
}

// The list runs refs before the quote, and each refusal wears its name.
func TestRewriteInputOrderAndNaming(t *testing.T) {
	a := &Agent{id: "abcd1234", figLog: quoteLog(t)}
	if _, err := a.rewriteInput(context.Background(), "<9>! hi"); err == nil || err.Error() != "quote: <9>: no message at lt 9" {
		t.Fatalf("%v", err)
	}
	if _, err := a.rewriteInput(context.Background(), "<2.x>! hi"); err == nil || !strings.HasPrefix(err.Error(), "quote: <2.x>: block") {
		t.Fatalf("%v", err)
	}
	if _, err := a.rewriteInput(context.Background(), "@zip! hi"); err == nil || err.Error() != "ref: @zip! is not on the board" {
		t.Fatalf("%v", err)
	}
	out, err := a.rewriteInput(context.Background(), "<2.0:0-5>! what is this")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "> quoting aria abcd1234 · turn 1 · lt 2.0 · chars 0-5 (5 chars)\n> Alpha\n\nwhat is this") {
		t.Fatalf("got %q", out)
	}
	// Unterminated tokens are text and pass through, whatever they look like.
	for _, in := range []string{"<2.0:0-5> what", "@zip what", "<3 you", ""} {
		if out, err := a.rewriteInput(context.Background(), in); err != nil || out != in {
			t.Fatalf("%q: %q %v", in, out, err)
		}
	}
}
