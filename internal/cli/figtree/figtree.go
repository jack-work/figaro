// Package figtree renders a tree of records as an indented table: one row per
// node, tree glyphs in the first cell, named fields in the columns after it.
//
// It exists because two callers need the same picture of a hierarchy: `figaro
// ls` drawing the aria forest, and an outfit layer closure explaining which
// layer could not be found, and a tree walker copied twice is a tree walker
// that will disagree with itself.
//
// Colour is data here, not code. A caller supplies FieldColors: "look at this
// field, and if its value is that, paint the label this role." The role names
// are the ones internal/term publishes, so a tree cannot invent a colour the
// rest of figaro does not already use.
package figtree

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/term"
)

// Node is one record in the tree. Fields carries everything the columns and
// the colour rules read, so adding a column never means changing this type.
type Node struct {
	// Marker is an optional glyph between the branch glyphs and the label
	// (`figaro ls` uses it for ●/▸/○). Omitted when empty, along with the
	// space that would follow it.
	Marker string

	// Label is the node's name, the only cell a reader is guaranteed to see:
	// it survives every width fallback.
	Label string

	// Fields are the node's named values, keyed as the Columns and
	// FieldColors name them. Nil is legal, a missing field reads as "".
	Fields map[string]string

	Children []*Node
}

// ColorRule paints Color when the watched field equals Value.
type ColorRule struct {
	Value string
	Color string
}

// FieldColor watches one field and paints the label by its value. Rules are
// tried in order; the first match wins, and no match leaves the label plain.
type FieldColor struct {
	FieldName string
	Rules     []ColorRule
}

// Column is one printed column. An empty Field means the tree cell itself
// (branch glyphs, marker, label), which is why it is normally listed first.
// Max truncates the value to that many runes; 0 leaves it whole.
type Column struct {
	Header string
	Field  string
	Max    int
}

// Tree is a whole renderable picture: what to walk, what to print, how to
// paint it. Columns nil renders the bare tree with no header.
type Tree struct {
	Roots       []*Node
	Columns     []Column
	Colors      []FieldColor
	Backgrounds []RowBackground
}

// RowBackground washes an ENTIRE row: glyphs, label, every padded cell,
// through to the right edge: with a raw SGR sequence when the watched
// field holds the value. figaro uses it to give unbound-form rows the
// same wash the transcript's node selection uses, one shared token.
type RowBackground struct {
	Field string
	Value string
	Seq   string
}

// Row is one flattened node. Branch/Marker/Label are kept apart so a caller
// can lay them out itself (a narrow fallback, say) rather than take Cell.
type Row struct {
	Branch string
	Marker string
	Label  string
	Fields map[string]string

	// Color is the role resolved from the Tree's FieldColors, "" for none.
	Color string

	// Background is the raw SGR wash resolved from the Tree's
	// Backgrounds, "" for none. Applied by RenderRows over the whole line.
	Background string
}

// Cell is the tree cell: branch glyphs, marker, then the label painted with
// Color. Only the label is painted: colouring the glyphs would make the shape
// of the tree compete with the meaning of its rows.
func (r Row) Cell() string {
	label := r.Label
	if r.Color != "" {
		label = term.Paint(r.Color, label)
	}
	out := r.Branch + r.Marker
	if r.Marker != "" && label != "" {
		out += " "
	}
	return out + label
}

// Field reads a field, "" when absent.
func (r Row) Field(name string) string { return r.Fields[name] }

// Rows flattens the tree depth-first, assigning each node its branch glyphs.
func (t Tree) Rows() []Row {
	var rows []Row
	var walk func(n *Node, prefix string, isLast, isRoot bool)
	walk = func(n *Node, prefix string, isLast, isRoot bool) {
		if n == nil {
			return
		}
		branch := ""
		if !isRoot {
			branch = prefix + "├─"
			if isLast {
				branch = prefix + "└─"
			}
		}
		rows = append(rows, Row{
			Branch:     branch,
			Marker:     n.Marker,
			Label:      n.Label,
			Fields:     n.Fields,
			Color:      t.colorOf(n),
			Background: t.backgroundOf(n),
		})
		childPrefix := prefix
		if !isRoot {
			if isLast {
				childPrefix += "  "
			} else {
				childPrefix += "│ "
			}
		}
		for i, c := range n.Children {
			walk(c, childPrefix, i == len(n.Children)-1, false)
		}
	}
	for _, r := range t.Roots {
		walk(r, "", true, true)
	}
	return rows
}

// backgroundOf resolves a node's row wash: first matching rule wins.
func (t Tree) backgroundOf(n *Node) string {
	for _, b := range t.Backgrounds {
		if n.Fields[b.Field] == b.Value {
			return b.Seq
		}
	}
	return ""
}

// colorOf resolves a node's role: first FieldColor with a matching rule wins.
func (t Tree) colorOf(n *Node) string {
	for _, fc := range t.Colors {
		value, ok := n.Fields[fc.FieldName]
		if !ok {
			continue
		}
		for _, rule := range fc.Rules {
			if rule.Value == value {
				return rule.Color
			}
		}
	}
	return ""
}

// Render lays the rows out in columns and clips each line to width. Width 0
// leaves lines unclipped.
//
// The padding is computed here rather than by text/tabwriter because tabwriter
// measures a cell in runes and counts an ANSI escape as several of them: one
// painted label silently widens its whole column. Measuring with
// term.VisibleLen instead is both colour-correct and, for unpainted text,
// identical to what tabwriter produced (TestRenderMatchesTabwriter pins that).
func (t Tree) Render(width int) string {
	return RenderRows(t.Rows(), t.Columns, width)
}

// RenderRows lays out rows a caller has already flattened, and possibly
// filtered or truncated, which is why this is not folded into Render.
func RenderRows(rows []Row, columns []Column, width int) string {
	if len(columns) == 0 {
		var out bytes.Buffer
		for _, r := range rows {
			fmt.Fprintln(&out, washRow(clip(r.Cell(), width), r.Background))
		}
		return out.String()
	}

	grid := make([][]string, 0, len(rows)+1)
	header := make([]string, len(columns))
	for i, c := range columns {
		header[i] = c.Header
	}
	grid = append(grid, header)
	for _, r := range rows {
		cells := make([]string, len(columns))
		for i, c := range columns {
			if c.Field == "" {
				cells[i] = r.Cell()
				continue
			}
			cells[i] = truncRunes(r.Fields[c.Field], c.Max)
		}
		grid = append(grid, cells)
	}

	widths := make([]int, len(columns))
	for _, cells := range grid {
		for i, cell := range cells {
			if n := term.VisibleLen(cell); n > widths[i] {
				widths[i] = n
			}
		}
	}

	var out bytes.Buffer
	for rowIdx, cells := range grid {
		var line strings.Builder
		for i, cell := range cells {
			line.WriteString(cell)
			// The final column is not padded: trailing blanks are invisible
			// and would only widen every line toward the clip.
			if i < len(cells)-1 {
				line.WriteString(strings.Repeat(" ", widths[i]-term.VisibleLen(cell)+padding))
			}
		}
		bg := ""
		if rowIdx > 0 {
			bg = rows[rowIdx-1].Background
		}
		fmt.Fprintln(&out, washRow(clip(line.String(), width), bg))
	}
	return out.String()
}

// washRow wraps a rendered line in a background sequence, re-arming the
// wash after any embedded reset (the idiom the transcript's selection
// wash established) and painting to the right edge with EL so the row
// reads as one band, not a ragged run of cells.
func washRow(line, bg string) string {
	if bg == "" || line == "" {
		return line
	}
	const reset = "\x1b[0m"
	line = strings.ReplaceAll(line, reset, reset+bg)
	return bg + line + "\x1b[K" + reset
}

// padding is the gap between columns, matching what `figaro ls` printed when
// it built this table with text/tabwriter.
const padding = 2

func clip(s string, width int) string {
	if width <= 0 || term.VisibleLen(s) <= width {
		return s
	}
	return term.TruncateVisible(s, width)
}

// truncRunes shortens s to max runes, matching the ".." ellipsis `figaro ls`
// already used for its narrow OUTFIT column. max 0 means no limit.
func truncRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max < 2 {
		return string(r[:max])
	}
	return string(r[:max-2]) + ".."
}
