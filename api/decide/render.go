package decide

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/rpc"
)

const Marker = "←"

func Lines(r rpc.DecideResponse) []string {
	width := 0
	for _, row := range r.Rows {
		width = max(width, utf8.RuneCountInString(row.Option))
	}
	out := make([]string, 0, len(r.Rows)+1)
	for _, row := range r.Rows {
		pad := strings.Repeat(" ", width-utf8.RuneCountInString(row.Option))
		mark := ""
		if row.Chosen {
			mark = "  " + Marker
		}
		out = append(out, fmt.Sprintf("%s%s  %.2f%s", row.Option, pad, row.Probability, mark))
	}
	if len(r.Rows) == 0 {
		out = append(out, "no answer")
	}
	return out
}

func Footer(r rpc.DecideResponse) string {
	parts := []string{}
	if r.Score != nil {
		parts = append(parts, fmt.Sprintf("score %.2f", *r.Score))
	}
	if r.Confidence != nil {
		parts = append(parts, fmt.Sprintf("confidence %.2f", *r.Confidence))
	}
	parts = append(parts, r.Model)
	w := r.Window
	ctx := fmt.Sprintf("%d messages", w.Messages)
	if w.Messages == 1 {
		ctx = "1 message"
	}
	if w.Focus != "" {
		ctx += " around " + w.Focus
	}
	parts = append(parts, ctx)
	if len(w.FormRefs) > 0 {
		parts = append(parts, "@"+strings.Join(w.FormRefs, " @"))
	}
	parts = append(parts, fmt.Sprintf("%d tokens in", r.InputToks))
	return strings.Join(parts, " · ")
}

func Render(r rpc.DecideResponse) string {
	var b strings.Builder
	for _, l := range Lines(r) {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	b.WriteString(Footer(r))
	b.WriteByte('\n')
	return b.String()
}
