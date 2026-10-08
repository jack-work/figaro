package quote

import (
	"fmt"
	"strings"
)

// A quote BLOCK is what a reader of the conversation sees where a reader of
// the raw log sees a coordinate: a heading naming the passage, then the
// passage itself under a gutter.
//
//	> quoting <412.0:23-1180>! · aria 7e151902 · turn 9 · 1157 chars
//	> Ecco fatto. The barber works in three strokes, …
//	>
//	> … and the third is the one nobody runs.
//
// Heading writes that first line and Mentioned reads it back, in this file,
// because the terminal has to draw a quote as chrome rather than as a
// paragraph of markdown and the only thing it is given is the text.

const Sep = " · "

const headingVerb = "quoting"

func Heading(r Range, aria string, turn uint64, total int) string {
	var b strings.Builder
	b.WriteString(headingVerb)
	b.WriteByte(' ')
	b.WriteString(Format(r))
	if aria != "" {
		b.WriteString(Sep + "aria " + aria)
	}
	if turn != 0 {
		fmt.Fprintf(&b, Sep+"turn %d", turn)
	}
	fmt.Fprintf(&b, Sep+"%d chars", total)
	return b.String()
}

func Block(gutter, heading, passage string) string {
	var b strings.Builder
	if heading != "" {
		b.WriteString(gutter)
		b.WriteString(heading)
		b.WriteByte('\n')
	}
	for _, line := range strings.Split(strings.TrimRight(passage, "\n"), "\n") {
		b.WriteString(gutter)
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.String()
}

type Mention struct {
	Range  Range
	Known  bool
	Detail string
	Body   string
}

func Mentioned(text string) (Mention, string, bool) {
	lines := strings.Split(text, "\n")
	var passage []string
	i := 0
	for ; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], ">") {
			break
		}
		passage = append(passage, strings.TrimPrefix(strings.TrimPrefix(lines[i], ">"), " "))
	}
	if len(passage) == 0 || !strings.HasPrefix(passage[0], headingVerb) {
		return Mention{}, text, false
	}
	m := Mention{}
	detail := strings.TrimSpace(strings.TrimPrefix(passage[0], headingVerb))
	if r, rest, err := Parse(detail); err == nil {
		m.Range, m.Known = r, true
		detail = strings.TrimPrefix(strings.TrimSpace(rest), strings.TrimSpace(Sep))
	}
	m.Detail = strings.TrimSpace(detail)
	m.Body = strings.Join(passage[1:], "\n")
	return m, strings.TrimLeft(strings.Join(lines[i:], "\n"), "\n"), true
}

func (m Mention) Brief() string {
	if !m.Known {
		return "quoted"
	}
	s := fmt.Sprintf("lt %d", m.Range.Start.LT)
	if m.Range.Block {
		s += fmt.Sprintf(".%d", m.Range.Start.Block)
	}
	if m.Range.Span() {
		s += fmt.Sprintf("–%d.%d", m.Range.End.LT, m.Range.End.Block)
	}
	return s
}
