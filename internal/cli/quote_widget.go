package cli

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

const (
	quoteOpen       = "╭"
	quoteClose      = "╰"
	quoteMark       = "❝"
	quoteBriefRows  = 3
	quoteHeadingCol = "  "
)

func quoteRows(m quote.Mention, width int, expanded bool) []string {
	body := render.Prose(m.Body, quoteBodyWidth(width))
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	hidden := 0
	if !expanded && len(body) > quoteBriefRows {
		hidden = len(body) - quoteBriefRows
		body = body[:quoteBriefRows]
	}
	out := []string{quoteHeadingCol + term.Dim(quoteOpen+" "+quoteMark+" "+quoteHeading(m, expanded))}
	gutter := term.Dim(quoteGutter)
	for _, r := range body {
		out = append(out, gutter+dedentProse(r))
	}
	if hidden > 0 {
		out = append(out, quoteHeadingCol+term.Dim(fmt.Sprintf("%s %s more", quoteClose, plural(hidden, "row"))))
	}
	return out
}

func quoteHeading(m quote.Mention, expanded bool) string {
	if !expanded || m.Detail == "" {
		return m.Brief()
	}
	if !m.Known {
		return m.Detail
	}
	return m.Brief() + quote.Sep + m.Detail
}

func quoteFolds(inquiry string, width int) bool {
	m, _, ok := quote.Mentioned(inquiry)
	if !ok {
		return false
	}
	return len(quoteRows(m, width, true)) > len(quoteRows(m, width, false))
}

func quoteBodyWidth(width int) int {
	if w := width - quoteGutterCells; w > 0 {
		return w
	}
	return width
}
