package figtree

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"text/tabwriter"

	"github.com/jack-work/figaro/internal/term"
)

// Render pads its own columns because tabwriter counts ANSI escapes as width;
// unpainted output must still match it.
func TestRenderMatchesTabwriter(t *testing.T) {
	tree := Tree{
		Columns: []Column{
			{Header: "ARIA"},
			{Header: "ID", Field: "id"},
			{Header: "OUTFIT", Field: "outfit"},
			{Header: "CTX", Field: "ctx"},
		},
		Roots: []*Node{{
			Marker: "●",
			Label:  "a root with a fairly long mantra",
			Fields: map[string]string{"id": "aaaa1111", "outfit": "opus5-ant", "ctx": "12k"},
			Children: []*Node{
				{Marker: "○", Label: "short", Fields: map[string]string{"id": "bb", "outfit": "x", "ctx": "-"}},
				{Marker: "▸", Label: "middle child", Fields: map[string]string{"id": "cccc3333", "outfit": "sonn5", "ctx": "~9k"},
					Children: []*Node{
						{Marker: "○", Label: "deep", Fields: map[string]string{"id": "dddd", "outfit": "sonn5", "ctx": "1k"}},
					}},
				{Marker: "○", Label: "last", Fields: map[string]string{"id": "eeee", "outfit": "", "ctx": ""}},
			},
		}},
	}

	rows := tree.Rows()
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ARIA\tID\tOUTFIT\tCTX")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Cell(), r.Field("id"), r.Field("outfit"), r.Field("ctx"))
	}
	w.Flush()

	if got, want := tree.Render(0), buf.String(); got != want {
		t.Fatalf("figtree padding diverged from tabwriter\n--- figtree ---\n%s\n--- tabwriter ---\n%s", got, want)
	}
}

func TestRowsDrawTheExpectedGlyphs(t *testing.T) {
	tree := Tree{Roots: []*Node{{
		Label: "root",
		Children: []*Node{
			{Label: "first", Children: []*Node{
				{Label: "first-kid"},
				{Label: "first-last"},
			}},
			{Label: "last", Children: []*Node{
				{Label: "last-kid"},
			}},
		},
	}}}

	var got []string
	for _, r := range tree.Rows() {
		got = append(got, r.Cell())
	}
	want := []string{
		"root",
		"├─first",
		"│ ├─first-kid",
		"│ └─first-last",
		"└─last",
		"  └─last-kid",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("glyphs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestColorRulesPaintTheLabelByFieldValue(t *testing.T) {
	restore := term.SetColorMode(term.ColorAlways)
	defer restore()

	tree := Tree{
		Colors: []FieldColor{{
			FieldName: "exists",
			Rules: []ColorRule{
				{Value: "yes", Color: "present"},
				{Value: "no", Color: "absent"},
			},
		}},
		Roots: []*Node{{
			Label:  "root",
			Fields: map[string]string{"exists": "yes"},
			Children: []*Node{
				{Label: "gone", Fields: map[string]string{"exists": "no"}},
				{Label: "unknown", Fields: map[string]string{"exists": "maybe"}},
				{Label: "unwatched"},
			},
		}},
	}

	rows := tree.Rows()
	if rows[0].Color != "present" || rows[1].Color != "absent" {
		t.Fatalf("resolved colors = %q, %q", rows[0].Color, rows[1].Color)
	}
	if rows[2].Color != "" || rows[3].Color != "" {
		t.Fatalf("unmatched nodes were painted: %q, %q", rows[2].Color, rows[3].Color)
	}

	if !strings.Contains(rows[0].Cell(), term.Present("root")) {
		t.Fatalf("root not painted present: %q", rows[0].Cell())
	}
	if !strings.Contains(rows[1].Cell(), term.Absent("gone")) {
		t.Fatalf("missing node not painted absent: %q", rows[1].Cell())
	}
	if strings.HasPrefix(rows[1].Cell(), "\033") {
		t.Fatalf("branch glyphs were painted: %q", rows[1].Cell())
	}
}

func TestPaintedLabelsDoNotShiftColumns(t *testing.T) {
	build := func() Tree {
		return Tree{
			Columns: []Column{{Header: "NAME"}, {Header: "WHERE", Field: "path"}},
			Colors: []FieldColor{{
				FieldName: "exists",
				Rules:     []ColorRule{{Value: "no", Color: "absent"}},
			}},
			Roots: []*Node{{
				Label:  "root",
				Fields: map[string]string{"exists": "yes", "path": "/a"},
				Children: []*Node{
					{Label: "a-long-missing-name", Fields: map[string]string{"exists": "no", "path": "/b"}},
				},
			}},
		}
	}

	off := term.SetColorMode(term.ColorNever)
	plain := build().Render(0)
	off()

	on := term.SetColorMode(term.ColorAlways)
	painted := build().Render(0)
	on()

	var stripped []string
	for _, line := range strings.Split(painted, "\n") {
		stripped = append(stripped, stripANSI(line))
	}
	if got := strings.Join(stripped, "\n"); got != plain {
		t.Fatalf("colour changed the layout\n--- painted, stripped ---\n%s\n--- plain ---\n%s", got, plain)
	}
}

func stripANSI(s string) string {
	var out []rune
	inEsc := false
	for _, r := range s {
		if inEsc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
			continue
		}
		if r == '\033' {
			inEsc = true
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
