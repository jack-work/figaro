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
	case rpc.FormatAge:
		var ms int64
		if json.Unmarshal(raw, &ms) == nil && ms > 0 {
			return ago(time.Since(time.UnixMilli(ms)))
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

type viewRowKey struct {
	group string
	row   string
}

type viewGroup struct {
	node *formNode
	rows []*formNode
}

func (v *formView) viewGroups(list *formNode) []viewGroup {
	view := v.view
	keep := func(ns []*formNode) []*formNode {
		out := make([]*formNode, 0, len(ns))
		for _, n := range ns {
			if view.Skip != "" && viewCell(n, rpc.ViewColumn{Key: view.Skip}) == "true" {
				continue
			}
			out = append(out, n)
		}
		sortViewRows(out, view.Sort)
		return out
	}
	if view.Rows == "" {
		return []viewGroup{{rows: keep(list.kids())}}
	}
	var out []viewGroup
	for _, g := range list.kids() {
		vg := viewGroup{node: g}
		if holder := formChild(g, view.Rows); holder != nil && !holder.leaf() {
			vg.rows = keep(holder.kids())
		}
		out = append(out, vg)
	}
	return out
}

func (v *formView) marked(g, r *formNode) bool {
	m := v.view.Mark
	if m == nil {
		return false
	}
	if m.RowSubject == "" {
		return m.Key != "" && viewCell(r, rpc.ViewColumn{Key: m.Key}) == m.Equals
	}
	if v.subject == nil {
		return false
	}
	snap := v.subject()
	want := func(key string) string {
		if p := snap.Lookup(key); p != nil {
			return *p
		}
		return ""
	}
	if r.label != want(m.RowSubject) {
		return false
	}
	return m.GroupSubject == "" || (g != nil && g.label == want(m.GroupSubject))
}

func (v *formView) tableItems(list *formNode, width int) []pitRow {
	view := v.view
	groups := v.viewGroups(list)

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
	for _, g := range groups {
		for _, r := range g.rows {
			mark := " "
			if v.marked(g.node, r) {
				mark = "●"
			}
			line := []string{mark}
			for _, c := range view.Columns {
				line = append(line, viewCell(r, c))
			}
			cells = append(cells, line)
		}
	}
	lines := tabulate(cells, cols, max(width-pitPrefixWidth, 1))

	v.rows = v.rows[:0]
	v.rowKeys = map[string]viewRowKey{}
	out := []pitRow{staticRow(lines[0])}
	next := 1
	for _, g := range groups {
		groupLabel := ""
		if g.node != nil {
			groupLabel = g.node.label
			out = append(out, v.groupRow(g.node, width))
		}
		for _, r := range g.rows {
			r.width = width
			key := viewRowKey{group: groupLabel, row: r.label}
			v.rowKeys[r.path] = key
			row := pitRow{text: lines[next], yankFrom: r, id: r.path}
			next++
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
				v.rowKeys[n.path] = key
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
	}
	if len(view.Actions) > 0 {
		var hints []string
		for _, a := range view.Actions {
			hints = append(hints, a.Key+" "+a.Label)
		}
		out = append(out, staticRow(""), staticRow(clipLine("  "+strings.Join(hints, " · "), width)))
	}
	return out
}

func (v *formView) groupRow(g *formNode, width int) pitRow {
	cells := []string{g.label}
	alert := false
	for _, c := range v.view.GroupColumns {
		cell := viewCell(g, c)
		if c.Alert && cell != "" && cell != "-" {
			alert = true
		}
		if cell == "-" {
			cell = ""
		}
		cells = append(cells, cell)
	}
	cols := make([]tableCol, len(cells))
	for i := 1; i < len(cols); i++ {
		cols[i].shrink = 1
	}
	line := tabulate([][]string{cells}, cols, max(width-pitPrefixWidth, 1))[0]
	row := pitRow{text: line}
	if alert {
		row.tone = toneError
	}
	return row
}
