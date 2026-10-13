// Package figtree renders a tree of records as an indented table.
package figtree

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/term"
)

type Node struct {
	// Marker is an optional glyph between the branch glyphs and the label.
	Marker string

	// Label survives every width fallback.
	Label string

	// Fields is keyed as Columns and FieldColors name them; a missing field
	// reads as "".
	Fields map[string]string

	Children []*Node
}

// ColorRule paints Color when the watched field equals Value.
type ColorRule struct {
	Value string
	Color string
}

// FieldColor paints the label by one field's value; first matching rule wins.
type FieldColor struct {
	FieldName string
	Rules     []ColorRule
}

// Column with an empty Field is the tree cell itself; Max 0 leaves the value whole.
type Column struct {
	Header string
	Field  string
	Max    int
}

// Tree with nil Columns renders the bare tree with no header.
type Tree struct {
	Roots       []*Node
	Columns     []Column
	Colors      []FieldColor
	Backgrounds []RowBackground
}

// RowBackground washes the whole row to the right edge with a raw SGR sequence
// when the field holds Value.
type RowBackground struct {
	Field string
	Value string
	Seq   string
}

// Row keeps Branch/Marker/Label apart so a caller can lay them out itself.
type Row struct {
	Branch string
	Marker string
	Label  string
	Fields map[string]string

	Color      string
	Background string
}

// Cell paints only the label, never the branch glyphs.
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

func (r Row) Field(name string) string { return r.Fields[name] }

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

func (t Tree) backgroundOf(n *Node) string {
	for _, b := range t.Backgrounds {
		if n.Fields[b.Field] == b.Value {
			return b.Seq
		}
	}
	return ""
}

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

// Render clips each line to width; 0 leaves lines unclipped.
func (t Tree) Render(width int) string {
	return RenderRows(t.Rows(), t.Columns, width)
}

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
			// The final column is not padded.
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

// washRow re-arms the wash after every embedded reset and paints to the right edge
// with EL.
func washRow(line, bg string) string {
	if bg == "" || line == "" {
		return line
	}
	const reset = "\x1b[0m"
	line = strings.ReplaceAll(line, reset, reset+bg)
	return bg + line + "\x1b[K" + reset
}

// padding matches text/tabwriter's gap, which `figaro ls` printed before.
const padding = 2

func clip(s string, width int) string {
	if width <= 0 || term.VisibleLen(s) <= width {
		return s
	}
	return term.TruncateVisible(s, width)
}

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
