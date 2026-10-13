package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/partialjson"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

type toolStyle struct {
	Label    string // replaces the tool's name on the minimized header
	Headline string // the argument that rides the header, in the call colour
	Body     string // the argument drawn in place of the tool's output
	Diff     bool   // declared, never guessed from the shape of a line
}

var toolStyles = map[string]toolStyle{
	"bash":    {Label: "$", Headline: "command"},
	"process": {Label: "$", Headline: "command"},
	"write":   {Headline: "path", Body: "content"},
	"read":    {Headline: "path"},
	"edit":    {Headline: "path", Diff: true},
}

func styleFor(name string) toolStyle { return toolStyles[name] }

// toolArgFields sorts by name in both phases, or the block reshuffles the instant
// the arguments land.
func toolArgFields(n livedoc.Node) []partialjson.Field {
	if strings.TrimSpace(n.Input) != "" {
		f := partialjson.Fields([]byte(n.Input))
		sort.Slice(f, func(i, j int) bool { return f[i].Name < f[j].Name })
		return f
	}
	keys := make([]string, 0, len(n.Args))
	for k := range n.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []partialjson.Field
	for _, k := range keys {
		out = flattenArg(out, k, n.Args[k], 0)
	}
	return out
}

// flattenArg names nested arguments by path: `edits[0].new_text`.
func flattenArg(out []partialjson.Field, name string, v any, depth int) []partialjson.Field {
	const maxDepth = 4
	switch t := v.(type) {
	case string:
		return append(out, partialjson.Field{Name: name, Value: t, Done: true})
	case map[string]any:
		if depth >= maxDepth || len(t) == 0 {
			break
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = flattenArg(out, name+"."+k, t[k], depth+1)
		}
		return out
	case []any:
		if depth >= maxDepth || len(t) == 0 {
			break
		}
		for i, e := range t {
			out = flattenArg(out, fmt.Sprintf("%s[%d]", name, i), e, depth+1)
		}
		return out
	}
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(fmt.Sprintf("%v", v))
	}
	return append(out, partialjson.Field{Name: name, Value: string(b), Done: true})
}

// pick falls back to the first argument when the tool has no such name.
func pick(fields []partialjson.Field, name string, settled bool) (partialjson.Field, bool) {
	for _, f := range fields {
		if f.Name == name {
			return f, true
		}
	}
	if settled && len(fields) > 0 {
		return fields[0], true
	}
	return partialjson.Field{}, false
}

const (
	toolGutter      = "  │ "
	toolGutterCells = 4
)

// toolBody: clamp -1 keeps every row. Only output is scanned for diff fences,
// never a declared body argument, which is a file.
func toolBody(n livedoc.Node, st toolStyle, fields []partialjson.Field, clamp int) []bodyLine {
	text, fenced := n.Output, true
	if st.Body != "" {
		f, ok := pick(fields, st.Body, false)
		if !ok {
			return nil
		}
		text, fenced = f.Value, false
	}
	text = strings.TrimRight(render.SanitizeForTerminal(text), "\n")
	if text == "" {
		return nil
	}
	var rows []bodyLine
	if fenced && render.HasDiff(text) {
		for _, l := range render.FencedLines(text) {
			rows = append(rows, bodyLine{text: l.Text, diff: l.Diff})
		}
	} else {
		for _, l := range strings.Split(text, "\n") {
			rows = append(rows, bodyLine{text: l, diff: st.Diff})
		}
	}
	total := len(rows)
	if clamp >= 0 && total > clamp {
		rows = append([]bodyLine{{text: term.Dim(fmt.Sprintf("… last %d of %d lines", clamp, total))}},
			rows[total-clamp:]...)
	}
	return rows
}

type bodyLine struct {
	text string
	diff bool
}

func renderToolNode(n livedoc.Node, width, bashCap int, tick uint64, expand bool) []string {
	st := styleFor(n.Name)
	fields := toolArgFields(n)
	settled := strings.TrimSpace(n.Input) == ""
	settledResult := n.FinishedAt != 0 ||
		n.Status == livedoc.StatusOK || n.Status == livedoc.StatusError
	headline, hasHeadline := pick(fields, st.Headline, settled)

	glyph := term.Green("✓")
	switch n.Status {
	case livedoc.StatusError:
		glyph = term.Red("✗")
	case livedoc.StatusRunning, "":
		frames := livedoc.SpinnerFrames
		glyph = term.Cyan(string(frames[int(tick)%len(frames)]))
	}
	dur := ""
	if d := toolElapsed(n); d != "" {
		dur = " " + term.Dim("["+d+"]")
	}

	// Minimized, the header carries the call; expanded, the tool's name, since
	// the call follows in full.
	name := n.Name
	if name == "" {
		name = "tool"
	}
	if st.Label != "" {
		name = st.Label
	}
	head := glyph + " " + term.Arg(name)
	if !expand && hasHeadline && headline.Value != "" {
		room := width - term.VisibleLen(head) - term.VisibleLen(dur) - 1
		head += " " + term.Body(clipToWidthEllipsis(oneLine(headline.Value), room))
	} else if expand {
		head = glyph + " " + term.Arg(n.Name)
	}
	rows := []string{clipToWidth(head+dur, width)}

	content := width - toolGutterCells
	row := func(s string) { rows = append(rows, term.Dim(toolGutter)+clipToWidth(s, content)) }

	if expand {
		if hasHeadline {
			for _, l := range hardWrap(render.SanitizeForTerminal(headline.Value), content) {
				row(term.Arg(l))
			}
		}
		for _, f := range fields {
			if f.Name == headline.Name || f.Name == st.Body {
				continue
			}
			// A multi-line argument is a block only until the result
			// lands; a yank still gives it whole.
			if !settledResult && strings.ContainsRune(f.Value, '\n') {
				row(term.Label(f.Name))
				for _, l := range argBlock(f.Value, content, bashCap) {
					row(l)
				}
				continue
			}
			row(argRow(f.Name, f.Value, content))
		}
		if n.StartedAt != 0 {
			row(term.Label("started ") + term.Body(formatToolTime(n.StartedAt)))
		}
		if n.FinishedAt != 0 {
			row(term.Label("finished ") + term.Body(formatToolTime(n.FinishedAt)))
		}
	}

	// Minimized, body rows are cut rather than wrapped.
	clamp := bashCap
	if expand {
		clamp = nodeOutputUnlimited
	}
	body := toolBody(n, st, fields, clamp)
	if len(body) > 0 && expand && len(rows) > 1 {
		row("")
	}
	for _, l := range body {
		paint := plainPaint
		if l.diff {
			paint = render.DiffPaint(l.text)
		}
		if expand {
			for _, w := range hardWrap(l.text, content) {
				row(paint(w))
			}
			continue
		}
		row(paint(clipToWidthEllipsis(l.text, content)))
	}
	return rows
}

func plainPaint(s string) string { return s }

func argRow(name, value string, width int) string {
	v := oneLine(render.SanitizeForTerminal(value))
	return term.Label(name) + " " + term.Body(clipToWidthEllipsis(v, width-len(name)-1))
}

func argBlock(value string, width, limit int) []string {
	text := strings.TrimRight(render.SanitizeForTerminal(value), "\n")
	if text == "" {
		return nil
	}
	shown, total := headOutput(text, limit)
	var out []string
	if limit >= 0 && total > limit {
		out = append(out, term.Dim(fmt.Sprintf("… first %d of %d lines", limit, total)))
	}
	for _, l := range strings.Split(shown, "\n") {
		for _, w := range hardWrap(l, width) {
			out = append(out, term.Body(w))
		}
	}
	return out
}

func headOutput(text string, limit int) (string, int) {
	total := 1 + strings.Count(text, "\n")
	if limit < 0 || total <= limit {
		return text, total
	}
	if limit == 0 {
		return "", total
	}
	at := -1
	for range limit {
		i := strings.IndexByte(text[at+1:], '\n')
		if i < 0 {
			return text, total
		}
		at += i + 1
	}
	return text[:at], total
}

// oneLine: a header row with a newline desyncs the painter's cursor math.
func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimRight(s[:i], " \t") + " ⏎"
	}
	return s
}
