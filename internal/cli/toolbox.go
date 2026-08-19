package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/partialjson"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

// How a tool is drawn.
type toolStyle struct {
	// Label replaces the tool's name on the minimized header. A shell is
	// better announced by its prompt than by the word "bash".
	Label string
	// Headline is the argument that speaks for the call: the command for a
	// shell, the path for a file tool. It rides the minimized header and opens
	// the expanded body, and it is the one argument drawn in the call colour.
	Headline string
	// Body is the argument to draw in place of the tool's own output. `write`
	// sets it to `content`, so the file body streams in exactly the way a
	// command's output does, and its receipt ("Wrote N bytes") is dropped,
	// since the content is the interesting half and the reader can see it.
	Body string
	// Diff paints the body's +/- lines. The tool declares that its output is a
	// diff; the renderer never guesses from the shape of a line.
	Diff bool
}

var toolStyles = map[string]toolStyle{
	"bash":    {Label: "$", Headline: "command"},
	"process": {Label: "$", Headline: "command"},
	"write":   {Headline: "path", Body: "content"},
	"read":    {Headline: "path"},
	"edit":    {Headline: "path", Diff: true},
}

// styleFor answers for every tool, named or not. An unknown tool keeps its
// name and takes its first argument as the headline, which is the same shape
// as the known ones rather than a special case.
func styleFor(name string) toolStyle { return toolStyles[name] }

// toolArgFields answers "what are this tool's arguments": the streaming JSON
// prefix while they arrive, the decoded map once they land: sorted by name in
// BOTH phases, since the streamed order is the model's and the settled order
// is a Go map's, and anything else reshuffles the block the instant the
// arguments land.
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

// flattenArg turns one argument into the fields a reader can actually read,
// naming nested ones by PATH: `edits[0].new_text` rather than `edits`.
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
	// A scalar that is not a string, or a nest too deep to name: JSON, which is
	// at least the notation the value arrived in. %v never is.
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(fmt.Sprintf("%v", v))
	}
	return append(out, partialjson.Field{Name: name, Value: string(b), Done: true})
}

// pick returns the named argument, or: when the tool has no such argument at
// all: the first one there is. The fallback is what lets `process` be drawn
// like a shell without having a `command`: it gets its `action`, the nearest
// thing it has to one.
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

// toolBody is the block's content: the argument a tool declares as its body,
// or its own output. clamp is the row budget; -1 for all of them.
func toolBody(n livedoc.Node, st toolStyle, fields []partialjson.Field, clamp int) []string {
	text := n.Output
	if st.Body != "" {
		f, ok := pick(fields, st.Body, false)
		if !ok {
			return nil
		}
		text = f.Value
	}
	text = strings.TrimRight(render.SanitizeForTerminal(text), "\n")
	if text == "" {
		return nil
	}
	shown, total := tailOutput(text, clamp)
	rows := strings.Split(shown, "\n")
	if clamp >= 0 && total > clamp {
		rows = append([]string{term.Dim(fmt.Sprintf("… last %d of %d lines", clamp, total))}, rows...)
	}
	return rows
}

func renderToolNode(n livedoc.Node, width, bashCap int, tick uint64, verbose, expand bool) []string {
	expand = expand || verbose
	st := styleFor(n.Name)
	fields := toolArgFields(n)
	settled := strings.TrimSpace(n.Input) == ""
	// settled says the arguments have landed; settledResult says the call has.
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

	// The header. Minimized it carries the call itself: `$ grep …`, `write
	// /tmp/x`: because that is what a reader scanning a transcript is looking
	// for. Expanded it steps back to the tool's name, since the call is about
	// to be shown in full on the row below.
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
		// The call, then everything else about it. The headline argument is
		// drawn in the call colour and without a label; the rest are labelled
		// and body-coloured, so the eye finds the command without reading.
		if hasHeadline {
			for _, l := range hardWrap(render.SanitizeForTerminal(headline.Value), content) {
				row(term.Arg(l))
			}
		}
		for _, f := range fields {
			if f.Name == headline.Name || f.Name == st.Body {
				continue
			}
			// While the call is still being written its arguments are all there
			// is to look at, so a multi-line one is a block, head-clamped. Once
			// the result lands it stands in for them and each argument shrinks
			// to one clipped row, an allusion to the call, not the payload.
			// A yank still gives them whole; see toolClipboardFull.
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

	// The body: the tool's output, or the argument that stands in for it.
	// Minimized it is clamped and each row is cut rather than wrapped, a
	// preview that reflows is harder to scan than one that stops.
	clamp := bashCap
	if expand {
		clamp = nodeOutputUnlimited
	}
	body := toolBody(n, st, fields, clamp)
	if len(body) > 0 && expand && len(rows) > 1 {
		row("") // one blank row, where the junction used to be
	}
	for _, l := range body {
		paint := diffPaint(st, l)
		if expand {
			for _, w := range hardWrap(l, content) {
				row(paint(w))
			}
			continue
		}
		row(paint(clipToWidthEllipsis(l, content)))
	}
	return rows
}

// argRow draws one argument as exactly one row: newlines flattened to a ⏎
// scar, the rest clipped to an ellipsis.
func argRow(name, value string, width int) string {
	v := oneLine(render.SanitizeForTerminal(value))
	return term.Label(name) + " " + term.Body(clipToWidthEllipsis(v, width-len(name)-1))
}

// argBlock draws a multi-line argument value: head-clamped to limit rows,
// wrapped to width, in the body voice.
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

// headOutput is tailOutput's twin, keeping the FIRST limit lines.
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

// diffPaint answers how one body line is coloured. A row is painted by its
// SOURCE line's marker, so a wrapped continuation keeps its side of the diff.
func diffPaint(st toolStyle, line string) func(string) string {
	if st.Diff {
		switch {
		case strings.HasPrefix(line, "+"):
			return term.DiffAdd
		case strings.HasPrefix(line, "-"):
			return term.DiffDel
		}
	}
	return func(s string) string { return s }
}

// oneLine flattens a value for a row that must be exactly one: a multi-line
// command in a header would desync the painter's one-row-per-line arithmetic.
func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimRight(s[:i], " \t") + " ⏎"
	}
	return s
}
