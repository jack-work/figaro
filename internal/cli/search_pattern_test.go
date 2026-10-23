package cli

import (
	"strings"
	"testing"
)

func TestSearchPatternKinds(t *testing.T) {
	for _, tc := range []struct {
		q       string
		text    string
		matches []string
	}{
		{"needle", "a Needle and a needle", []string{"Needle", "needle"}},
		{"Needle", "a Needle and a needle", []string{"Needle"}},
		{`ne+dle`, "nedle needle neeedle", []string{"nedle", "needle", "neeedle"}},
		{`Ne+dle`, "Needle needle", []string{"Needle"}},
		{`\bfox\w*`, "foxtrot outfox fox", []string{"foxtrot", "fox"}},
		{"foo(", "call foo( here", []string{"foo("}},
		{"[x", "a [x b", []string{"[x"}},
		{"a*", "baaab", []string{"aaa"}},
		{"élan", "ÉLAN élan", []string{"ÉLAN", "élan"}},
	} {
		p := compileSearch(tc.q)
		var got []string
		for _, s := range p.spans(tc.text) {
			got = append(got, tc.text[s[0]:s[1]])
		}
		if strings.Join(got, "|") != strings.Join(tc.matches, "|") {
			t.Errorf("%q over %q matched %q, want %q", tc.q, tc.text, got, tc.matches)
		}
	}
}

// The pit's rows and the composer's draft are searched with the same pattern
// as the transcript.
func TestPitAndComposerSearchWithThePattern(t *testing.T) {
	p := newPicker([]pitRow{
		{text: "  alpha-1", id: "a"},
		{text: "  beta-22", id: "b"},
		{text: "  gamma-333", id: "c"},
	})
	if !p.find(`-\d{3}`, 1) || p.rows[p.cursor].id != "c" {
		t.Fatalf("a regex did not find the pit row: cursor %d", p.cursor)
	}
	e := ed("one Two three two", 0)
	if !e.findRune("two", 1) || e.cursor != 4 {
		t.Fatalf("lowercase is case-blind: cursor %d", e.cursor)
	}
	if !e.findRune("two", 1) || e.cursor != 14 {
		t.Fatalf("the next match on the line: cursor %d", e.cursor)
	}
	if !e.findRune("Two", 1) || e.cursor != 4 {
		t.Fatalf("an uppercase letter makes it case-exact: cursor %d", e.cursor)
	}
}
