package decide

import (
	"reflect"
	"testing"
)

func TestParseTakesTheLastLineToTheEndOfTheMessage(t *testing.T) {
	body, opts, ok := Parse("which way do we jump?\n?? ship revert wait")
	if !ok {
		t.Fatal("sigil on the last line was not found")
	}
	if body != "which way do we jump?" {
		t.Fatalf("body = %q", body)
	}
	if want := []string{"ship", "revert", "wait"}; !reflect.DeepEqual(opts, want) {
		t.Fatalf("options = %#v, want %#v", opts, want)
	}
}

func TestParseAcceptsTheOpeningBraceWithNoCloser(t *testing.T) {
	// Gluck's own grammar: "_dec{option1 option2 is legal, no trailing curly
	// brace". The brace is decoration, not structure.
	for _, in := range []string{"?? {a b c", "??{a b c", "??{a b c}", "?? { a b c }"} {
		_, opts, ok := Parse("pick\n" + in)
		if !ok {
			t.Fatalf("%q: not found", in)
		}
		if want := []string{"a", "b", "c"}; !reflect.DeepEqual(opts, want) {
			t.Fatalf("%q: options = %#v", in, opts)
		}
	}
}

func TestParseRespectsQuotes(t *testing.T) {
	_, opts, ok := Parse("?? \"ship it now\" 'hold the line' wait")
	if !ok {
		t.Fatal("not found")
	}
	want := []string{"ship it now", "hold the line", "wait"}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("options = %#v, want %#v", opts, want)
	}
}

func TestParseKeepsAQuotedBraceOutOfTheStructure(t *testing.T) {
	_, opts, ok := Parse("?? {\"a }b\" c")
	if !ok {
		t.Fatal("not found")
	}
	if want := []string{"a }b", "c"}; !reflect.DeepEqual(opts, want) {
		t.Fatalf("options = %#v", opts)
	}
}

func TestParseIgnoresASigilThatIsNotOnTheLastLine(t *testing.T) {
	// The whole collision argument for `??` rests on this: a sigil anywhere
	// but the tail is prose.
	body, _, ok := Parse("?? a b c\nand then some prose about it")
	if ok {
		t.Fatal("a sigil in the middle of a message claimed the options")
	}
	if body != "?? a b c\nand then some prose about it" {
		t.Fatalf("body = %q", body)
	}
}

func TestParseIgnoresASigilInsideACodeFence(t *testing.T) {
	// Pasted TypeScript is the realistic collision: `??` is nullish
	// coalescing, and a formatter will put it at the head of a line.
	src := "why is this undefined?\n```ts\nconst v = a\n  ?? b ?? c\n```"
	body, _, ok := Parse(src)
	if ok {
		t.Fatal("a fenced `??` was read as an option list")
	}
	if body != src {
		t.Fatalf("body = %q", body)
	}
}

func TestParseTreatsAnUnclosedFenceAsFencedToTheEnd(t *testing.T) {
	src := "look:\n```go\nx := a\n?? b c"
	if _, _, ok := Parse(src); ok {
		t.Fatal("an unclosed fence stopped protecting its contents")
	}
}

func TestParseEscapeHatch(t *testing.T) {
	body, _, ok := Parse("literal, please\n\\?? not options")
	if ok {
		t.Fatal("the escape did not suppress the sigil")
	}
	if body != "literal, please\n?? not options" {
		t.Fatalf("body = %q", body)
	}
}

func TestParseWithNoOptionsIsNotAnOptionList(t *testing.T) {
	// A bare sigil is how a reader asks the composer for the options line, so
	// it must not read as "a choice with no choices".
	for _, in := range []string{"??", "?? ", "??{}", "?? { }"} {
		if _, opts, ok := Parse("decide\n" + in); ok || len(opts) != 0 {
			t.Fatalf("%q: ok=%v opts=%#v", in, ok, opts)
		}
	}
}

func TestParseDedupesPreservingOrder(t *testing.T) {
	// Option ORDER is part of what Jev sees (jev-1.13 leans toward the first
	// option), so the parser may not sort, and a repeat may not become two
	// mutually exclusive options with the same name.
	_, opts, _ := Parse("?? b a b c a")
	if want := []string{"b", "a", "c"}; !reflect.DeepEqual(opts, want) {
		t.Fatalf("options = %#v, want %#v", opts, want)
	}
}

func TestParseLeavesAnEmptyBodyWhenTheMessageIsOnlyOptions(t *testing.T) {
	body, opts, ok := Parse("?? yes no")
	if !ok || len(opts) != 2 {
		t.Fatalf("ok=%v opts=%#v", ok, opts)
	}
	if body != "" {
		t.Fatalf("body = %q", body)
	}
}

func TestParseKeepsBlankLinesOutOfTheWay(t *testing.T) {
	body, opts, ok := Parse("ship it?\n\n??  ship   wait \n\n")
	if !ok {
		t.Fatal("trailing blank lines hid the sigil")
	}
	if body != "ship it?" {
		t.Fatalf("body = %q", body)
	}
	if want := []string{"ship", "wait"}; !reflect.DeepEqual(opts, want) {
		t.Fatalf("options = %#v", opts)
	}
}

func TestAppendRoundTrips(t *testing.T) {
	cases := [][]string{
		{"a", "b"},
		{"ship it now", "wait"},
		{`say "no"`, "yes"},
		{"trailing space ", "x"},
		{"}brace{", "y"},
	}
	for _, opts := range cases {
		text := Append("the question", opts)
		body, got, ok := Parse(text)
		if !ok {
			t.Fatalf("%#v: Append produced %q, which does not parse", opts, text)
		}
		if body != "the question" {
			t.Fatalf("%#v: body = %q", opts, body)
		}
		want := make([]string, 0, len(opts))
		for _, o := range opts {
			want = append(want, trimSpace(o))
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%#v: round trip = %#v", opts, got)
		}
	}
}

func TestAppendOnAnEmptyBody(t *testing.T) {
	if got := Append("", []string{"a"}); got != "?? a" {
		t.Fatalf("Append = %q", got)
	}
}

func TestAppendWithNoOptionsChangesNothing(t *testing.T) {
	if got := Append("ask", nil); got != "ask" {
		t.Fatalf("Append = %q", got)
	}
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// The client asks this to decide whether the composer wants an options line,
// and the provider asks it to filter a broker's catalog: one predicate, in the
// package both sides already import, so the two cannot disagree.
func TestIsModel(t *testing.T) {
	decisions := []string{
		"jev", "jev-latest", "jev-preview", "jev-1.13.0",
		"typesafe/jev-1.13", "~typesafe/jev-latest", "explabs/jev-latest",
	}
	prose := []string{
		"claude-opus-5", "gpt-5.6-luna", "jevons-paradox", "anthropic/claude-sonnet-4-6",
		"", "auto",
	}
	for _, m := range decisions {
		if !IsModel(m) {
			t.Errorf("%s should be a decision model", m)
		}
	}
	for _, m := range prose {
		if IsModel(m) {
			t.Errorf("%s should NOT be a decision model", m)
		}
	}
}

func TestBareFindsASigilWithNoOptions(t *testing.T) {
	// The composer opens its options line on exactly this, so it must be the
	// same "last line, not fenced" rule Parse applies, and nothing looser.
	for _, in := range []string{"pick one\n??", "pick one\n?? ", "pick one\n??{", "pick one\n??{}"} {
		body, ok := Bare(in)
		if !ok {
			t.Fatalf("%q: bare sigil not found", in)
		}
		if body != "pick one" {
			t.Fatalf("%q: body = %q", in, body)
		}
	}
	for _, in := range []string{"pick one\n?? a b", "pick one", "?? x\nmore prose", "```\n??\n"} {
		if _, ok := Bare(in); ok {
			t.Fatalf("%q: reported a bare sigil", in)
		}
	}
}

func TestChoicesSplitsOnCommasOutsideQuotes(t *testing.T) {
	// -C="option 1","option 2" reaches the program as `option 1,option 2`
	// once a shell (or the pager's tokenizer) has eaten the quotes, and as
	// the literal text when they survive. Both must mean two options.
	cases := []struct {
		in   string
		want []string
	}{
		{`option 1,option 2`, []string{"option 1", "option 2"}},
		{`"option 1","option 2"`, []string{"option 1", "option 2"}},
		{`=option 1,option 2`, []string{"option 1", "option 2"}},
		{`"a, b",c`, []string{"a, b", "c"}},
		{`'x, y' , z`, []string{"x, y", "z"}},
		{` yes , no `, []string{"yes", "no"}},
		{`a,,b,`, []string{"a", "b"}},
		{`@skills`, []string{"@skills"}},
		{``, nil},
	}
	for _, c := range cases {
		if got := Choices(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Choices(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestRefNamesAFormKey(t *testing.T) {
	cases := []struct {
		in, key string
		ok      bool
	}{
		{"@skills", "skills", true},
		{"@skills.bossman.filePath", "skills.bossman.filePath", true},
		{"@skills!", "skills", true},
		{"@{skills}", "skills", true},
		{"@", "", false},
		{"skills", "", false},
		{"email@host", "", false},
	}
	for _, c := range cases {
		key, ok := Ref(c.in)
		if ok != c.ok || key != c.key {
			t.Errorf("Ref(%q) = %q,%v want %q,%v", c.in, key, ok, c.key, c.ok)
		}
	}
}

func TestYesNoIsTheBinaryPairInEitherOrderAndCase(t *testing.T) {
	for _, in := range [][]string{{"yes", "no"}, {"No", "YES"}, {"y", "n"}} {
		if !YesNo(in) {
			t.Errorf("%#v is not yes/no", in)
		}
	}
	for _, in := range [][]string{{"yes"}, {"yes", "no", "maybe"}, {"yes", "maybe"}, nil} {
		if YesNo(in) {
			t.Errorf("%#v read as yes/no", in)
		}
	}
}
