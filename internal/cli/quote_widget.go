package cli

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

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
	body := passageRows(m.Body, quoteBodyWidth(width))
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
		out = append(out, gutter+r)
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

const quoteTabStop = 4

func passageRows(body string, width int) []string {
	if preformatted(body) {
		return verbatimRows(body, width)
	}
	var rows []string
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			rows = append(rows, "")
			continue
		}
		for _, r := range render.Prose(line, width) {
			rows = append(rows, dedentProse(r))
		}
	}
	return rows
}

func preformatted(body string) bool {
	if strings.Contains(body, "```") {
		return true
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "  ") {
			return true
		}
	}
	return false
}

func verbatimRows(body string, width int) []string {
	var rows []string
	for _, line := range strings.Split(render.StripEscapes(body), "\n") {
		for _, r := range hangWrap(strings.TrimRight(expandTabs(line), " \r"), max(width, 1)) {
			rows = append(rows, term.Body(r))
		}
	}
	return rows
}

func expandTabs(line string) string {
	if !strings.ContainsRune(line, '\t') {
		return line
	}
	var b strings.Builder
	col := 0
	for _, r := range line {
		if r == '\t' {
			n := quoteTabStop - col%quoteTabStop
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col += runewidth.RuneWidth(r)
	}
	return b.String()
}

func hangWrap(line string, width int) []string {
	if runewidth.StringWidth(line) <= width {
		return []string{line}
	}
	body := strings.TrimLeft(line, " ")
	lead := line[:len(line)-len(body)]
	hang := lead[:min(len(lead), width/2)]
	var rows []string
	row, col := hang, len(hang)
	flush := func() {
		rows = append(rows, strings.TrimRight(row, " "))
		row, col = hang, len(hang)
	}
	for _, word := range strings.SplitAfter(body, " ") {
		w := runewidth.StringWidth(strings.TrimRight(word, " "))
		if col+w > width && strings.TrimSpace(row) != "" {
			flush()
		}
		for col+w > width {
			head := runewidth.Truncate(word, width-col, "")
			if head == "" {
				_, size := utf8.DecodeRuneInString(word)
				head = word[:size]
			}
			row += head
			word = word[len(head):]
			w = runewidth.StringWidth(strings.TrimRight(word, " "))
			flush()
		}
		row += word
		col += runewidth.StringWidth(word)
	}
	if strings.TrimSpace(row) != "" {
		rows = append(rows, strings.TrimRight(row, " "))
	}
	return rows
}
