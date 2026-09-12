package quote

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAndFormatRoundTrip(t *testing.T) {
	cases := []struct {
		in   string
		want Range
		rest string
	}{
		{"<412>!", Range{Start: Coord{LT: 412}, End: Coord{LT: 412}}, ""},
		{"<412.0>!", Range{Start: Coord{LT: 412}, End: Coord{LT: 412}, Block: true}, ""},
		{"<412.2>! and more", Range{Start: Coord{LT: 412, Block: 2}, End: Coord{LT: 412, Block: 2}, Block: true}, "and more"},
		{"<412.0:23-1180>!", Range{
			Start: Coord{LT: 412, Block: 0, Offset: 23}, End: Coord{LT: 412, Block: 0, Offset: 1180},
			Block: true, Offsets: true}, ""},
		{"<412.0:23-419.2:88>!\n\tq", Range{
			Start: Coord{LT: 412, Block: 0, Offset: 23}, End: Coord{LT: 419, Block: 2, Offset: 88},
			Block: true, Offsets: true}, "q"},
		{"<412.0:5-5>!", Range{
			Start: Coord{LT: 412, Offset: 5}, End: Coord{LT: 412, Offset: 5},
			Block: true, Offsets: true}, ""},
	}
	for _, c := range cases {
		got, rest, err := Parse(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%q: parsed %+v, want %+v", c.in, got, c.want)
		}
		if rest != c.rest {
			t.Fatalf("%q: rest %q, want %q", c.in, rest, c.rest)
		}
		back := Format(got)
		if again, _, err := Parse(back); err != nil || again != got {
			t.Fatalf("%q: Format gave %q which parses to %+v (%v)", c.in, back, again, err)
		}
	}
}

// Text that does not carry a terminated token is text, and Parse must say so
// with ErrNone rather than with a refusal: a reader who writes "<3" or pastes
// an HTML tag has not asked for a quote.
func TestUnterminatedIsText(t *testing.T) {
	for _, in := range []string{
		"", "hello", "<412>", "<412> hello", "<412.0:23-1180> not terminated",
		"<3 you", "<div>hi</div>", " <412>!", "!<412>",
	} {
		_, rest, err := Parse(in)
		if !errors.Is(err, ErrNone) {
			t.Fatalf("%q: err %v, want ErrNone", in, err)
		}
		if rest != in {
			t.Fatalf("%q: rest %q, want the text untouched", in, rest)
		}
	}
}

// A terminated token that is not a coordinate is refused, and the reason
// names what is wrong. Every row here is a spelling somebody will type.
func TestMalformedIsRefused(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<>!", "empty coordinate"},
		{"<abc>!", `lt "abc" is not a number`},
		{"<0>!", "lt 0 is not a message"},
		{"<-1>!", `lt "-1" is not a number`},
		{"<412.>!", "missing block after '.'"},
		{"<412.x>!", `block "x" is not a non-negative number`},
		{"<412.-1>!", `block "-1" is not a non-negative number`},
		{"<412:23-1180>!", "offsets need a block"},
		{"<412.0:23>!", "offsets are start-end"},
		{"<412.0:-1180>!", "missing start offset"},
		{"<412.0:23->!", "missing end offset"},
		{"<412.0:a-b>!", `start offset "a" is not a non-negative number`},
		{"<412.0:23-x>!", `end offset "x" is not a non-negative number`},
		{"<412.0:30-20>!", "end 20 is before start 30"},
		{"<412.0:23-400:88>!", "span end needs a block"},
		{"<412.0:23-400.1:88>!", "span end is before its start"},
		{"<412.1:23-412.0:88>!", "span end is before its start"},
		{"<412.0:23-abc.1:88>!", `span end: lt "abc" is not a number`},
		{"<412 .0>!", "no spaces"},
		{"<412.0 : 23-1180>!", "no spaces"},
	}
	for _, c := range cases {
		_, _, err := Parse(c.in)
		if err == nil || errors.Is(err, ErrNone) {
			t.Fatalf("%q: accepted (err=%v), want a refusal mentioning %q", c.in, err, c.want)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%q: %q does not mention %q", c.in, err, c.want)
		}
		if !strings.HasPrefix(err.Error(), "quote: <") {
			t.Fatalf("%q: %q should start with the token it refuses", c.in, err)
		}
	}
}

func TestSpan(t *testing.T) {
	same := Range{Start: Coord{LT: 1, Offset: 0}, End: Coord{LT: 1, Offset: 9}, Block: true, Offsets: true}
	if same.Span() {
		t.Fatal("a range inside one block is not a span")
	}
	across := Range{Start: Coord{LT: 1}, End: Coord{LT: 1, Block: 1}, Block: true, Offsets: true}
	if !across.Span() {
		t.Fatal("a range across two blocks is a span")
	}
	whole := Range{Start: Coord{LT: 1}, End: Coord{LT: 1}}
	if whole.Span() {
		t.Fatal("a whole-message range is not a span")
	}
}
