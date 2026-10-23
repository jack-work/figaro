// Package render turns markdown into ANSI terminal rows: a pure function of
// (markdown, width), no I/O.
package render

import (
	"log/slog"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// Prose synth-closes a trailing unclosed fence so a streaming code block keeps a stable shape.
func Prose(md string, width int) []string {
	// glamour drops the ESC of an unknown escape and prints its parameter bytes.
	md = StripEscapes(md)
	if strings.Count(md, "```")%2 == 1 {
		md += "\n```"
	}
	if rows, ok := lookupProse(md, width); ok {
		return rows
	}
	rows := hardWrapOverlong(SanitizeRows(proseRows(md, width)), width)
	storeProse(md, width, rows)
	return append([]string(nil), rows...)
}

// diffIndent matches glamour's inset for a fenced code block.
const diffIndent = "    "

// proseRows draws fenced diff regions itself and leaves the rest to glamour.
func proseRows(md string, width int) []string {
	if !HasDiff(md) {
		return renderMarkdown(md, width)
	}
	var out, run []string
	inDiff := false
	flush := func() {
		if len(run) == 0 {
			return
		}
		if inDiff {
			out = append(out, "")
			for _, r := range DiffRows(strings.Join(run, "\n"), width-len(diffIndent)) {
				out = append(out, diffIndent+r)
			}
			out = append(out, "")
		} else {
			out = append(out, renderMarkdown(strings.Join(run, "\n"), width)...)
		}
		run = run[:0]
	}
	for _, l := range FencedLines(md) {
		if l.Diff != inDiff {
			flush()
			inDiff = l.Diff
		}
		run = append(run, l.Text)
	}
	flush()
	return trimBlankEdges(out)
}

// renderMarkdown falls back to plain wrapped text on a glamour error or panic.
func renderMarkdown(text string, width int) (rows []string) {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	plain := func() []string {
		var out []string
		for _, l := range strings.Split(text, "\n") {
			out = append(out, wrapPlain(l, width)...)
		}
		return trimBlankEdges(out)
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("markdown render panicked; falling back to plain text",
				"panic", r, "width", width, "bytes", len(text))
			rows = plain()
		}
	}()
	out, err := renderLocked(text, width)
	if err != nil || out == "" {
		return plain()
	}
	return trimBlankEdges(strings.Split(strings.TrimRight(out, "\n"), "\n"))
}

func renderLocked(text string, width int) (string, error) {
	rendererMu.Lock()
	defer rendererMu.Unlock()
	return rendererForLocked(width).Render(text)
}

var (
	rendererMu    sync.Mutex
	rendererCache = map[int]*markdownRenderer{}
)

// Caller holds rendererMu, and keeps holding it across Render.
func rendererForLocked(width int) *markdownRenderer {
	if r, ok := rendererCache[width]; ok {
		return r
	}
	r := newMarkdownRenderer(width)
	rendererCache[width] = r
	return r
}

func wrapPlain(line string, width int) []string {
	if width <= 0 || runewidth.StringWidth(line) <= width {
		return []string{line}
	}
	var rows []string
	var b strings.Builder
	col := 0
	for _, r := range line {
		w := runewidth.RuneWidth(r)
		if col+w > width {
			rows = append(rows, b.String())
			b.Reset()
			col = 0
		}
		b.WriteRune(r)
		col += w
	}
	if b.Len() > 0 || len(rows) == 0 {
		rows = append(rows, b.String())
	}
	return rows
}

func trimBlankEdges(rows []string) []string {
	for len(rows) > 0 && visiblyBlank(rows[0]) {
		rows = rows[1:]
	}
	for len(rows) > 0 && visiblyBlank(rows[len(rows)-1]) {
		rows = rows[:len(rows)-1]
	}
	return rows
}

// visiblyBlank discounts escapes: glamour pads a block_quote with a row of styled spaces.
func visiblyBlank(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == 0x1b {
			for i++; i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')); i++ {
			}
		} else if c > ' ' {
			return false
		}
	}
	return true
}

func cells(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		r, sz := utf8.DecodeRuneInString(s[i:])
		n += runewidth.RuneWidth(r)
		i += sz
	}
	return n
}

// hardWrapOverlong breaks the rows glamour will not.
func hardWrapOverlong(rows []string, width int) []string {
	if width <= 0 {
		return rows
	}
	over := false
	for _, r := range rows {
		if cells(r) > width {
			over = true
			break
		}
	}
	if !over {
		return rows
	}
	out := make([]string, 0, len(rows)+2)
	for _, r := range rows {
		if cells(r) <= width {
			out = append(out, r)
			continue
		}
		out = append(out, splitToWidth(r, width)...)
	}
	return out
}

// splitToWidth carries the active SGR run onto each continuation.
func splitToWidth(row string, width int) []string {
	var out []string
	var chunk strings.Builder
	var style strings.Builder
	col := 0
	flush := func() {
		if chunk.Len() > 0 {
			out = append(out, chunk.String())
			chunk.Reset()
		}
		col = 0
		if style.Len() > 0 {
			chunk.WriteString(style.String())
		}
	}
	for i := 0; i < len(row); {
		if row[i] == 0x1b {
			j := skipEscape(row, i)
			seq := row[i:j]
			chunk.WriteString(seq)
			if strings.HasSuffix(seq, "m") {
				if seq == "\x1b[0m" || seq == "\x1b[m" {
					style.Reset()
				} else {
					style.WriteString(seq)
				}
			}
			i = j
			continue
		}
		r, sz := utf8.DecodeRuneInString(row[i:])
		w := runewidth.RuneWidth(r)
		if col+w > width {
			flush()
		}
		chunk.WriteRune(r)
		col += w
		i += sz
	}
	if chunk.Len() > 0 {
		out = append(out, chunk.String())
	}
	return out
}
