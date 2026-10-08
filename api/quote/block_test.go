package quote

import "testing"

// The grammar is written by the daemon and read by the terminal. Round-trip
// is the whole contract: a heading this file writes is a heading this file
// can read back, coordinate included, for every shape a Range has.
func TestHeadingRoundTrips(t *testing.T) {
	for _, in := range []string{
		"<412>!",
		"<412.0>!",
		"<412.0:23-1180>!",
		"<412.0:23-419.2:88>!",
	} {
		r, _, err := Parse(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		block := Block("> ", Heading(r, "7e151902", 9, 1157), "Ecco fatto.\n\nAnd the third.")
		m, rest, ok := Mentioned(block + "what did you mean?")
		if !ok {
			t.Fatalf("%s: a block this package wrote was not recognised:\n%s", in, block)
		}
		if !m.Known || Format(m.Range) != in {
			t.Fatalf("%s: coordinate came back as %q (known=%v)", in, Format(m.Range), m.Known)
		}
		if m.Detail != "aria 7e151902 · turn 9 · 1157 chars" {
			t.Fatalf("%s: detail %q", in, m.Detail)
		}
		if m.Body != "Ecco fatto.\n\nAnd the third." {
			t.Fatalf("%s: body %q", in, m.Body)
		}
		if rest != "what did you mean?" {
			t.Fatalf("%s: rest %q", in, rest)
		}
	}
}

func TestBriefNamesTheCoordinate(t *testing.T) {
	cases := map[string]string{
		"<412>!":               "lt 412",
		"<412.0>!":             "lt 412.0",
		"<412.2:23-1180>!":     "lt 412.2",
		"<412.0:23-419.2:88>!": "lt 412.0–419.2",
	}
	for in, want := range cases {
		r, _, err := Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		m, _, ok := Mentioned(Block("> ", Heading(r, "", 0, 10), "body"))
		if !ok {
			t.Fatalf("%s: not recognised", in)
		}
		if got := m.Brief(); got != want {
			t.Fatalf("%s: brief %q, want %q", in, got, want)
		}
	}
}

// Prose that merely OPENS with a markdown blockquote is prose. Without this,
// every quoted line a reader types by hand would be drawn as chrome naming a
// passage that does not exist.
func TestOrdinaryBlockquoteIsNotAMention(t *testing.T) {
	for _, in := range []string{
		"> as you said earlier\n\nwhat about it?",
		"hello\n> quoting something\n",
		"",
		"no quote at all",
	} {
		if _, rest, ok := Mentioned(in); ok || rest != in {
			t.Fatalf("%q was read as a quote block (rest %q)", in, rest)
		}
	}
}

// A heading written before this grammar existed still draws as a quote: the
// coordinate is unknown, and the heading it did write is what a verbose
// reader sees.
func TestHeadingFromBeforeTheGrammar(t *testing.T) {
	old := "> quoting aria 7e151902 · turn 9 · lt 412.0 · chars 23-1180 (1157 chars)\n> Ecco fatto.\n\nand?"
	m, rest, ok := Mentioned(old)
	if !ok {
		t.Fatal("an old block must still be recognised")
	}
	if m.Known {
		t.Fatalf("an old heading has no canonical coordinate: %+v", m.Range)
	}
	if m.Brief() != "quoted" {
		t.Fatalf("brief %q", m.Brief())
	}
	if m.Detail != "aria 7e151902 · turn 9 · lt 412.0 · chars 23-1180 (1157 chars)" {
		t.Fatalf("detail %q", m.Detail)
	}
	if m.Body != "Ecco fatto." || rest != "and?" {
		t.Fatalf("body %q rest %q", m.Body, rest)
	}
}

// A quote with no words after it is the whole message.
func TestMentionWithNothingAfterIt(t *testing.T) {
	r, _, err := Parse("<9.0>!")
	if err != nil {
		t.Fatal(err)
	}
	m, rest, ok := Mentioned(Block("> ", Heading(r, "", 0, 4), "body"))
	if !ok || rest != "" || m.Body != "body" {
		t.Fatalf("ok=%v rest=%q body=%q", ok, rest, m.Body)
	}
}
