package cli

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/modelcat"
	"github.com/mattn/go-runewidth"
)

type modelRow struct {
	ID string `json:"id"`
	models.Entry
}

type providerListing struct {
	Provider    string     `json:"provider"`
	RefreshedAt int64      `json:"refreshed_at,omitempty"`
	AttemptedAt int64      `json:"attempted_at,omitempty"`
	Error       string     `json:"error,omitempty"`
	Stale       bool       `json:"stale"`
	Models      []modelRow `json:"models"`
}

func catalogListings(catalog form.Snapshot, now time.Time) []providerListing {
	provs, _ := models.Providers(catalog)
	out := make([]providerListing, 0, len(provs))
	for name, p := range provs {
		l := providerListing{
			Provider:    name,
			RefreshedAt: p.RefreshedAt,
			AttemptedAt: p.AttemptedAt,
			Error:       p.Error,
			Stale:       p.RefreshedAt == 0 || now.Sub(time.UnixMilli(p.RefreshedAt)) >= modelcat.DefaultTTL,
			Models:      make([]modelRow, 0, len(p.Models)),
		}
		for id, e := range p.Models {
			l.Models = append(l.Models, modelRow{ID: id, Entry: e})
		}
		sort.Slice(l.Models, func(i, j int) bool { return l.Models[i].ID < l.Models[j].ID })
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

func (l providerListing) pickable() []modelRow {
	out := make([]modelRow, 0, len(l.Models))
	for _, m := range l.Models {
		if !m.Hidden {
			out = append(out, m)
		}
	}
	return out
}

func (l providerListing) failed() bool { return l.Error != "" }

func (l providerListing) status(now time.Time) string {
	since := func(ms int64) string { return ago(now.Sub(time.UnixMilli(ms))) }
	var parts []string
	switch {
	case l.RefreshedAt != 0 && l.Stale:
		parts = append(parts, "stale, listed "+since(l.RefreshedAt))
	case l.RefreshedAt != 0:
		parts = append(parts, "listed "+since(l.RefreshedAt))
	case !l.failed():
		parts = append(parts, "not listed yet")
	}
	if l.failed() {
		at := ""
		if l.AttemptedAt != 0 {
			at = " " + since(l.AttemptedAt)
		}
		parts = append(parts, "failed"+at+": "+l.Error)
	}
	return strings.Join(parts, "; ")
}

func (m modelRow) displayName() string {
	if strings.EqualFold(m.Name, m.ID) {
		return ""
	}
	return m.Name
}

func dashCount(n int) string {
	if n <= 0 {
		return "-"
	}
	return formatCtxCell(n)
}

type tableCol struct {
	right  bool
	shrink int
}

const tableGap = 2

func plainCells(rows [][]string) [][]string {
	out := make([][]string, len(rows))
	for n, r := range rows {
		out[n] = make([]string, len(r))
		for i, cell := range r {
			out[n][i] = pitText(cell)
		}
	}
	return out
}

func tabulate(rows [][]string, cols []tableCol, width int) []string {
	rows = plainCells(rows)
	widths := make([]int, len(cols))
	for _, r := range rows {
		for i, cell := range r {
			widths[i] = max(widths[i], runewidth.StringWidth(cell))
		}
	}
	if width > 0 {
		shrinkWidths(widths, cols, width)
	}
	out := make([]string, len(rows))
	for n, r := range rows {
		var b strings.Builder
		for i, cell := range r {
			if widths[i] == 0 {
				continue
			}
			if b.Len() > 0 {
				b.WriteString(strings.Repeat(" ", tableGap))
			}
			cell = clipToWidthEllipsis(cell, widths[i])
			pad := strings.Repeat(" ", widths[i]-runewidth.StringWidth(cell))
			if cols[i].right {
				b.WriteString(pad + cell)
			} else {
				b.WriteString(cell + pad)
			}
		}
		line := strings.TrimRight(b.String(), " ")
		if width > 0 {
			line = clipToWidthEllipsis(line, width)
		}
		out[n] = line
	}
	return out
}

const minShrunkCol = 8

func shrinkWidths(widths []int, cols []tableCol, width int) {
	total := 0
	for _, w := range widths {
		if w > 0 {
			total += w + tableGap
		}
	}
	excess := total - tableGap - width
	order := make([]int, 0, len(cols))
	for i, c := range cols {
		if c.shrink > 0 {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return cols[order[a]].shrink > cols[order[b]].shrink })
	for _, floor := range []int{minShrunkCol, 1} {
		for _, i := range order {
			if excess <= 0 {
				return
			}
			cut := min(excess, widths[i]-floor)
			if cut > 0 {
				widths[i] -= cut
				excess -= cut
			}
		}
	}
}

var modelCols = []tableCol{{}, {shrink: 1}, {shrink: 2}, {right: true}, {right: true}, {}}

func modelCells(mark string, m modelRow) []string {
	return []string{mark, m.ID, m.displayName(), dashCount(m.Window("")), dashCount(m.MaxOutput), m.sources()}
}

func (m modelRow) sources() string {
	var out []string
	for _, f := range []string{"context", "max_output"} {
		if s := m.Source(f); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, "+")
}
