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
//
// WHERE THIS LIVES, and why: one table, in the CLI, keyed by the tool NAME the
// wire already carries. It is presentation policy — the server has no business
// knowing that a shell wants a `$` — and it is the only place any of it is
// written down. A second frontend lifts this table rather than the renderer.
//
// It replaces the per-tool `Summarize()` methods that used to live beside the
// tools themselves: those were the same idea (which argument speaks for this
// call) split across the server, the projector and four files.
//
//	minimized                        expanded
//	⠧ $ grep -n bass opera.md [4ms]  ✓ bash [1.4s]
//	  │ … last 10 of 32 lines          │ grep -n bass opera.md
//	  │ 15:13. Figaro is a baritone.    │ started 2026-08-06 01:33:48.347 EDT
//	                                    │ finished 2026-08-06 01:33:48.365 EDT
//	                                    │
//	                                    │ 15:13. Figaro is a baritone.
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
	// command's output does — and its receipt ("Wrote N bytes") is dropped,
	// since the content is the interesting half and the reader can see it.
	Body string
}

var toolStyles = map[string]toolStyle{
	"bash":    {Label: "$", Headline: "command"},
	"process": {Label: "$", Headline: "command"},
	"write":   {Headline: "path", Body: "content"},
	"read":    {Headline: "path"},
	"edit":    {Headline: "path"},
}

// styleFor answers for every tool, named or not. An unknown tool keeps its
// name and takes its first argument as the headline, which is the same shape
// as the known ones rather than a special case.
func styleFor(name string) toolStyle { return toolStyles[name] }

// toolArgFields answers "what are this tool's arguments" — the streaming JSON
// prefix while they arrive, the decoded map once they land — sorted by name in
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
//
// It used to be `fmt.Sprintf("%v", v)` for anything that was not a string,
// which is Go's syntax, not the model's: an `edit` rendered as
//
//	edits [map[new_text:// render draws one block… old_text:…]]
//
// — map order unspecified, the strings' newlines and tabs collapsed into one
// unreadable line, and the actual edit invisible at any width or expansion.
// Worse, it was a REGRESSION at the moment of settling: while the arguments
// were still arriving the streaming parser showed the raw JSON, which at
// least parsed by eye.
//
// Flattening instead of pretty-printing is what makes the strings readable:
// each leaf is handed over as ITS OWN VALUE, so the tool block wraps and
// clamps it exactly as it does `content` or `command` — real newlines, real
// tabs, no escapes. There is no per-tool control flow here and no tool name
// is consulted; `edit` simply happens to be the only shape that needed it.
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

// pick returns the named argument, or — when the tool has no such argument at
// all — the first one there is. The fallback is what lets `process` be drawn
// like a shell without having a `command`: it gets its `action`, the nearest
// thing it has to one.
//
// settled gates the fallback, and that gate matters: while the arguments are
// still arriving, an absent `path` means NOT YET, not never, and standing in
// the first field that happens to have landed would put the file's contents
// where its name belongs.
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

	// The header. Minimized it carries the call itself — `$ grep …`, `write
	// /tmp/x` — because that is what a reader scanning a transcript is looking
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
			row(term.Label(f.Name) + " " + term.Body(oneLine(f.Value)))
		}
		if n.StartedAt != 0 {
			row(term.Label("started ") + term.Body(formatToolTime(n.StartedAt)))
		}
		if n.FinishedAt != 0 {
			row(term.Label("finished ") + term.Body(formatToolTime(n.FinishedAt)))
		}
	}

	// The body: the tool's output, or the argument that stands in for it.
	// Minimized it is clamped and each row is cut rather than wrapped — a
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
		if expand {
			for _, w := range hardWrap(l, content) {
				row(w)
			}
			continue
		}
		row(clipToWidthEllipsis(l, content))
	}
	return rows
}

// oneLine flattens a value for a row that must be exactly one: a multi-line
// command in a header would desync the painter's one-row-per-line arithmetic.
func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimRight(s[:i], " \t") + " ⏎"
	}
	return s
}
