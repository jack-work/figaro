package cli

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

func formChild(n *formNode, path string) *formNode {
	if path == "" {
		return n
	}
	for _, seg := range strings.Split(path, ".") {
		var next *formNode
		for _, c := range n.kids() {
			if c.label == seg {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		n = next
	}
	return n
}

func viewCell(row *formNode, col rpc.ViewColumn) string {
	if col.Key == "" {
		return row.label
	}
	n := formChild(row, col.Key)
	if n == nil {
		return "-"
	}
	if !n.leaf() {
		if col.Format == rpc.FormatCount {
			return strconv.Itoa(n.count)
		}
		return "{…}"
	}
	raw := n.value
	switch col.Format {
	case rpc.FormatCount:
		var xs []json.RawMessage
		if json.Unmarshal(raw, &xs) == nil {
			return strconv.Itoa(len(xs))
		}
	case rpc.FormatTime:
		var ms int64
		if json.Unmarshal(raw, &ms) == nil && ms > 0 {
			return time.UnixMilli(ms).Local().Format("2006-01-02 15:04")
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func viewLess(a, b string) bool {
	x, xerr := strconv.ParseFloat(a, 64)
	y, yerr := strconv.ParseFloat(b, 64)
	if xerr == nil && yerr == nil {
		return x < y
	}
	return a < b
}

func sortViewRows(rows []*formNode, by string) {
	if by == "" {
		return
	}
	desc := strings.HasPrefix(by, "-")
	col := rpc.ViewColumn{Key: strings.TrimPrefix(by, "-")}
	keys := make(map[*formNode]string, len(rows))
	for _, r := range rows {
		keys[r] = viewCell(r, col)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if desc {
			return viewLess(keys[rows[j]], keys[rows[i]])
		}
		return viewLess(keys[rows[i]], keys[rows[j]])
	})
}

func (v *formView) tableItems(list *formNode, width int) []pitRow {
	view := v.view
	rows := append([]*formNode(nil), list.kids()...)
	sortViewRows(rows, view.Sort)

	cols := make([]tableCol, 0, len(view.Columns)+1)
	cols = append(cols, tableCol{})
	header := []string{""}
	for _, c := range view.Columns {
		cols = append(cols, tableCol{right: c.Right, shrink: c.Shrink})
		title := c.Title
		if title == "" {
			title = c.Key
		}
		header = append(header, title)
	}
	cells := [][]string{header}
	for _, r := range rows {
		mark := " "
		if view.Mark != nil && viewCell(r, rpc.ViewColumn{Key: view.Mark.Key}) == view.Mark.Equals {
			mark = "●"
		}
		line := []string{mark}
		for _, c := range view.Columns {
			line = append(line, viewCell(r, c))
		}
		cells = append(cells, line)
	}
	lines := tabulate(cells, cols, max(width-pitPrefixWidth, 1))

	v.rows = v.rows[:0]
	out := []pitRow{staticRow(lines[0])}
	for i, r := range rows {
		r.width = width
		row := pitRow{text: lines[i+1], yankFrom: r, id: r.path}
		if v.touched[r.path] {
			row.note = "pending"
		}
		out = append(out, row)
		v.rows = append(v.rows, r)
		if !v.open[r.path] {
			continue
		}
		for _, n := range flattenFormTree(r, v.open, nil) {
			n.width = width
			nested := pitRow{textFrom: n, yankFrom: n, id: n.path}
			if v.touched[n.path] {
				nested.note = "pending"
			} else if n.leaf() && v.readOnlyLocked(n.path) {
				nested.note = "read-only"
			}
			out = append(out, nested)
			v.rows = append(v.rows, n)
		}
	}
	return out
}
