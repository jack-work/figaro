package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/partialjson"
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

const (
	nodeBashCapDefault  = 10
	nodeOutputUnlimited = -1
	toolSummaryCap      = 80 // default truncation for a tool's summary line
)

// renderSettings is the consumer-side verbosity toggle. The wire/IR always
// carries the full data; this only affects display, so it can be flipped live
// (Ctrl-O) and the unit re-rendered. Thinking blocks are always shown (muted);
// verbose additionally expands tool inputs to the full wrapped command.
type renderSettings struct {
	verbose  bool
	jsonMode bool // -j / --json: emit a single {aria_id, ...} JSON line on stdout instead of a live render
	listen   bool // -l / --listen: auto-enter the transcript at startup
	// record is a path for the wire tape (testing). Empty is the ordinary
	// path: nothing is opened, nothing is wrapped. See internal/tape.
	record string
}

// renderNode draws ONE node. This is the single dispatch on node kind, shared
// by every view: `show` (via renderNodeList), the inline incipit and the
// transcript pager (via ariaView). It used to exist twice — ariaView switched
// on n.Type alone and so drew a turn's prompt as agent prose, putting the
// user's own question under the "< figaro" header while `show` correctly
// marked it "↳ input". Two renderers for one representation is the exact defect
// class turn addressing exists to remove; there is now one.
//
// Expansion arrives twice, because a tool now has TWO collapsible parts that
// answer to different policies. bashCap collapses the OUTPUT — and the incipit
// always passes it uncapped, since nothing there can un-collapse after the
// fact (architecture.md invariant #2). expanded collapses the ARGUMENTS, and
// the incipit must NOT force that one: a streaming argument inline is a moving
// window on something being typed, and its whole value is that it stays small
// until asked. In the pager both come from the same gesture.
func renderNode(n livedoc.Node, width, bashCap int, tick uint64, verbose, expanded bool) []string {
	switch {
	case n.Type == livedoc.NodeTool:
		// Either gesture expands a tool: Ctrl-O (global) or Enter on the
		// selection (per node). They were not the same flag, so selecting a
		// tool and pressing Enter expanded its OUTPUT and left its arguments
		// hidden behind a different key.
		return renderToolNode(n, width, bashCap, tick, verbose, expanded)
	case n.Type == livedoc.NodeThinking:
		return renderThinkingNode(n, width)
	// Steering is the only input-voice NODE there is — the inquiry is text on
	// the turn and never reaches here — so this marker cannot be mistaken for
	// the question that opened the turn.
	case n.Type == livedoc.NodeSteering:
		return renderSteeringNode(n, width)
	default:
		return renderProseNode(n, width)
	}
}

// nodeExpandable reports whether toggling a node would reveal anything, so a
// gesture with nothing to show is inert rather than a silent flag flip. Only a
// tool has a second form; prose renders whole at every width.
func nodeExpandable(n livedoc.Node) bool {
	if n.Type != livedoc.NodeTool {
		return false
	}
	// Output OR arguments. A tool whose arguments are still streaming has no
	// output yet and is precisely the node you most want to open — a running
	// write, to watch the file arrive — so an output-only test made Enter
	// inert on it. A settled tool hides its arguments until asked, so it has
	// something to reveal even when its output is short.
	return strings.TrimSpace(n.Output) != "" ||
		strings.TrimSpace(n.Input) != "" || len(n.Args) > 0
}

// renderTurnRows renders a whole exchange — the inquiry that opened the turn,
// then what the agent made of it — through the ONE composer every surface
// shares (ldrender.Composer, compose.go). `show` is its caller; the pager and
// the incipit reach the composer directly.
func renderTurnRows(m aria.Message, width int, tick uint64, set renderSettings) []string {
	return ldrender.Text(turnComposer(m.Turn, width, tick, set).Message(m, width))
}

// renderNodeList renders a node list with no turn chrome around it.
func renderNodeList(nodes []livedoc.Node, width int, tick uint64, set renderSettings) []string {
	return ldrender.Text(turnComposer(0, width, tick, set).Nodes(nodes, width))
}

// turnComposer is `show`'s composition: the shared shape, the shared chrome,
// and — under --details — the same per-block coordinate row Ctrl-O draws in the
// pager, instead of the timestamp line `show` used to invent for itself.
//
// Blocks are drawn EXPANDED, as the incipit draws them (Composer.Expanded nil):
// `show` is a one-shot dump the reader scrolls in their own terminal, with no
// viewport to husband and no gesture to un-collapse with, so a collapsed table
// there could not be recovered.
func turnComposer(turn, width int, tick uint64, set renderSettings) ldrender.Composer {
	c := ldrender.Composer{
		View:   &ariaView{settings: &set},
		Header: messageHeader,
		Rule:   func() string { return dimTransRule(width) },
		Sender: dimSender,
		Tick:   int(tick),
	}
	if set.verbose {
		c.Coord = func(block int, n livedoc.Node) string {
			return term.Dim(coordLabel(turn, block, nodeCoordAt(n)))
		}
	}
	return c
}

// proseIndent matches the inset render.Prose gives a paragraph, so metadata
// drawn beside prose lines up with it instead of floating left of it.
const proseIndent = "  "

// clipToWidth truncates a styled row to at most width display columns,
// passing ANSI escape sequences through uncounted and appending a reset so
// a cut mid-color doesn't bleed. Embedded control characters (newlines,
// tabs, CR) are flattened to spaces: a row must be exactly one physical
// line or it desyncs the painter's one-row-per-line cursor math (a
// multi-line bash command in a tool's arg summary is the common culprit).
//
// The overwhelmingly common case is a row that already fits and carries
// nothing to rewrite — every row of every frame is clipped, twice (once by
// the node renderer, once by the transcript's selection gutter), and almost
// none of them actually need clipping. clipFits proves the rewrite is a
// no-op with a single allocation-free byte scan; only rows that genuinely
// change go through clipToWidthRewrite.
func clipToWidth(s string, width int) string {
	if clipFits(s, width) {
		return s
	}
	return clipToWidthRewrite(s, width)
}

// clipFits reports whether clipToWidthRewrite(s, width) == s, i.e. whether
// the row is already within width, free of control characters, and valid
// UTF-8 (the rewrite decodes to runes, so an invalid byte would come back
// as U+FFFD and change the row). Pure scan: no allocation.
func clipFits(s string, width int) bool {
	col := 0
	for i := 0; i < len(s); {
		if c := s[i]; c >= 0x20 && c < 0x7f {
			// Run of printable ASCII: one column per byte, no need to
			// consult runewidth (asserted by TestClipFits...Assumption).
			start := i
			for i++; i < len(s) && s[i] >= 0x20 && s[i] < 0x7f; i++ {
			}
			col += i - start
			if col > width {
				return false
			}
			continue
		} else if c == 0x1b { // escape sequence: copied verbatim, uncounted
			j, ascii := escapeEnd(s, i)
			if !ascii && !utf8.ValidString(s[i:j]) {
				return false // would round-trip through U+FFFD
			}
			i = j
			continue
		} else if c < utf8.RuneSelf { // control char: rewritten to a space
			return false
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return false // invalid UTF-8: the rewrite substitutes U+FFFD
		}
		col += runewidth.RuneWidth(r)
		if col > width {
			return false
		}
		i += size
	}
	return true
}

// escapeEnd returns the index just past the ANSI escape sequence starting at
// s[i] (assumed to be ESC) — everything up to and including the first ASCII
// letter, or the end of the string — and whether that span was pure ASCII
// (in which case the caller can skip the UTF-8 validity check, which is
// otherwise a quarter of the scan's cost on glamour-styled rows). Byte-wise
// scanning is equivalent to the rune-wise scan it replaces: a multi-byte
// rune's bytes are all >= 0x80 and so can never be mistaken for the
// terminating letter.
func escapeEnd(s string, i int) (int, bool) {
	// ONE grammar, in render.SkipEscape. This used to scan to the first ASCII
	// letter, which is not what an escape is: a bare ESC swallowed the
	// character after it, so displayWidth UNDERCOUNTED and clipToWidth emitted
	// a row one cell past the edge — measured, clip to 10 gave 11 visible
	// cells. An OSC whose payload contains a letter ended early instead, which
	// clips short and loses text. Tool output reaches these clips with escapes
	// intact, because SanitizeForTerminal deliberately keeps SGR.
	end := render.SkipEscape(s, i)
	ascii := true
	for j := i; j < end; j++ {
		if s[j] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	return end, ascii
}

// clipToWidthRewrite is the general path: it materializes the clipped row.
// displayWidth is the column count of a row with escape sequences excluded —
// what the terminal will actually occupy. runewidth.StringWidth counts the
// bytes of an SGR run as characters, which over-measures every styled row (a
// dim wrapper alone is eight columns of nothing) and would shed footer tokens
// that fit perfectly well.
func displayWidth(s string) int {
	col := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j, _ := escapeEnd(s, i)
			i = j
			continue
		}
		if c := s[i]; c >= 0x20 && c < 0x7f {
			col++
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if size == 0 {
			break
		}
		if r >= 0x20 {
			col += runewidth.RuneWidth(r)
		}
		i += size
	}
	return col
}

// clipToWidthEllipsis is clipToWidth for a row the reader parses as a SENTENCE
// rather than as a picture — the footer's status line. A hard clip there ends
// mid-token with nothing to say anything was dropped ("cost 4.5k to"); one
// column spent on an ellipsis says it ("cost 4.5k…").
//
// Body rows keep the hard clip on purpose: they are a picture, an ellipsis in
// every wrapped paragraph would be noise, and prose is re-wrapped to the width
// rather than truncated at it.
func clipToWidthEllipsis(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(s) <= width && clipFits(s, width) {
		return s
	}
	if width == 1 {
		return "…\x1b[0m"
	}
	body := clipToWidthRewrite(s, width-1)
	body = strings.TrimSuffix(body, "\x1b[0m")
	return body + "…\x1b[0m"
}

func clipToWidthRewrite(s string, width int) string {
	col := 0
	var b strings.Builder
	clipped := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b { // copy the whole escape sequence, uncounted
			// render.SkipEscape, not a third hand-rolled scanner. This loop
			// used to advance to the first ASCII LETTER, so a bare ESC ate the
			// character after it and that character was written UNCOUNTED —
			// the row then rendered one cell PAST THE EDGE. Measured in a real
			// pane: clip to 10 produced 11 visible columns. An OSC ended early
			// for the same reason ("\x1b]0;title\x07" stopped at the 't' of
			// "title"), which clips the row short and loses text instead.
			j := render.SkipEscape(s, i)
			seq := s[i:j]
			if !utf8.ValidString(seq) {
				// Never put invalid UTF-8 on the wire: round-trip through runes
				// so stray bytes become U+FFFD, as this has always done.
				seq = string([]rune(seq))
			}
			b.WriteString(seq)
			i = j
			continue
		}
		r, sz := utf8.DecodeRuneInString(s[i:])
		if r < 0x20 || r == 0x7f { // control char → space (keeps the row one physical line)
			r = ' '
		}
		w := runewidth.RuneWidth(r)
		if col+w > width {
			clipped = true
			break
		}
		b.WriteRune(r)
		col += w
		i += sz
	}

	if clipped {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

func renderProseNode(n livedoc.Node, width int) []string {
	return nodeProseRows(n, width)
}

// renderThinkingNode renders extended-thinking text as a dim blockquote
// (glamour styles "> " spans), visually distinct from the agent's prose.
func renderThinkingNode(n livedoc.Node, width int) []string {
	return nodeProseRows(n, width)
}

// renderSteeringNode renders a user message injected mid-turn — a steering
// interjection — under a marked gutter so it reads as the user's voice inside
// the assistant's turn, distinct from prose and thinking.
func renderSteeringNode(n livedoc.Node, width int) []string {
	// Subdued relative to the inquiry, deliberately: steering nudges a train of
	// thought already in motion, it does not open a turn. The inquiry gets a run
	// header and full-strength prose; steering gets an inline marker and a dim
	// blockquote gutter, so it reads as an aside within the agent's stream.
	head := term.Dim("↳ input")
	if n.Sender != "" {
		head += " " + term.Dim("· "+n.Sender)
	}
	return append([]string{head}, nodeProseRows(n, width)...)
}

// nodeMarkdown is the markdown a non-tool node renders from — the node's own
// text, for every kind. Thinking and steering used to be wrapped in blockquote
// syntax here; they now reserve width and draw their rule themselves (see
// nodeProseRows), because glamour prefixes markdown LINES and figaro needs
// every rendered ROW ruled.
func nodeMarkdown(n livedoc.Node) string { return n.Markdown }

// quoteGutter is the rule figaro draws down the left of thinking and steering,
// on every rendered row, itself — see nodeProseRows for why glamour cannot.
const quoteGutter = "  │ "

// quoted reports whether a node draws under the gutter.
func quoted(t livedoc.NodeType) bool {
	return t == livedoc.NodeThinking || t == livedoc.NodeSteering
}

// proseWidth is the width a node's markdown is rendered at: the full width,
// less the gutter for quoted kinds. nodeExpandable and the renderers must both
// use it, or the predicate lies about what a row will hold.
func proseWidth(n livedoc.Node, width int) int {
	if w := width - quoteGutterCells; quoted(n.Type) && w > 0 {
		return w
	}
	return width
}

// quoteGutterCells is quoteGutter's width on screen: four columns, not four
// bytes — the rule is a three-byte rune.
//
// The full four are reserved even though the rule STANDS IN glamour's
// two-column margin and so usually costs only two. A row with no margin to
// stand in pays the whole four: a hard-wrap continuation chunk (see
// render.hardWrapOverlong) has none, and neither does a row glamour emits
// flush. Reserving two was measured and overflows — w=20, CJK, 22 cells in a
// 20-column viewport. Those two columns are recoverable by teaching the hard
// wrap to carry the leading margin onto its continuations; that is a separate
// change with its own test, not a constant to shave here.
const quoteGutterCells = 4

// nodeProseRows renders a non-tool node's markdown, whole.
func nodeProseRows(n livedoc.Node, width int) []string {
	rows := render.Prose(nodeMarkdown(n), proseWidth(n, width))
	if !quoted(n.Type) {
		return rows
	}
	// THE RULE IS DRAWN, NOT REPAIRED.
	//
	// Thinking used to be handed to glamour as markdown blockquote syntax, and
	// glamour applies a blockquote prefix per MARKDOWN LINE, not per rendered
	// ROW — so a paragraph long enough to wrap produced continuation rows with
	// no rule at all. Three attempts to repair those rows AFTERWARDS each failed
	// for one structural reason: putting a two-cell rule where glamour put a
	// two-cell inset needs two columns the row does not have, and no post-hoc
	// edit can create horizontal space. Only re-wrapping can. Those versions
	// drew the rule one column left of the block (read as missing indentation),
	// and then ate the right-hand end of any row without slack — "… +261 more
	// table lines" came back as "… +261 more tabl".
	//
	// CONTRACT: a row may exceed `width` only where glamour's OWN output at the
	// reserved width already does — a nested list, a fence, an unclosed fence
	// (by up to 7 cells). This function adds the gutter and nothing else, and
	// does not clip: every painter already owns its edge (renderNodeList at
	// width, plainNodeRow at t.w-1, the incipit at w), and a clip here was one
	// column STRICTER than the frame, deleting a character that was on screen.
	//
	// So the width is reserved BEFORE rendering and the rule is prefixed after.
	// Nothing to detect, nothing to restore, nothing to clip: the defect is
	// unrepresentable rather than tested for. Tool output has always drawn its
	// own gutter this way.
	dim := term.Dim(quoteGutter) // one styled rule, not one per row
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		// The rule STANDS IN glamour's own paragraph margin rather than sitting
		// on top of it. Prefixing without dedenting put thinking text at column
		// 6 while the prose around it starts at 2 — two columns of content lost
		// at every width, for nothing. Dedenting puts it at 4, which is where
		// the old blockquote had it.
		out = append(out, dim+dedentProse(r))
	}
	return out
}

// dedentProse removes one proseIndent from a rendered row, looking past the
// SGR runs glamour emits before the first visible column. The inset is uniform,
// so nested content keeps its relative depth.
//
// THE ESCAPES ARE NOT ALL AT THE FRONT. This used to skip a leading run of
// escapes and then test for two literal spaces, which held only because
// glamour v1 emitted its margin as one unbroken "  ". v2 splits it — space,
// SGR, space — so the prefix test missed, the row was not dedented, and the
// gutter cost four columns instead of two: thinking text at column 6 while the
// prose beside it starts at 4. The farmer's 24-shape gutter fuzz caught it at
// `emoji w=20 row 3`. So: consume two VISIBLE spaces, wherever the escapes
// fall, and keep every escape.
func dedentProse(row string) string {
	var keep strings.Builder
	i, dropped := 0, 0
	for i < len(row) && dropped < len(proseIndent) {
		if row[i] == 0x1b {
			j, _ := escapeEnd(row, i)
			keep.WriteString(row[i:j])
			i = j
			continue
		}
		if row[i] != ' ' {
			break
		}
		i++
		dropped++
	}
	if dropped < len(proseIndent) {
		return row // not inset: nothing to stand in
	}
	return keep.String() + row[i:]
}

// renderToolNode draws a tool as a widget with ZERO per-tool control flow:
// a status glyph, the tool name, and — when set — the producer's Summary
// (truncated for the header, wrapped in verbose mode); then any streamed
// output under a dim gutter, tail-clamped to bashCap lines. In verbose mode
// Args are also rendered generically as sorted key=value lines. The client
// never inspects n.Name.
func renderToolNode(n livedoc.Node, width, bashCap int, tick uint64, verbose, expand bool) []string {
	var glyph string
	switch n.Status {
	case livedoc.StatusOK:
		glyph = term.Green("✓")
	case livedoc.StatusError:
		glyph = term.Red("✗")
	default:
		frames := livedoc.SpinnerFrames
		glyph = term.Cyan(string(frames[int(tick)%len(frames)]))
	}
	name := n.Name
	if name == "" {
		name = "tool"
	}
	// The arguments — streaming or settled, folded or expanded, one shape.
	args := toolArgRows(n, width, expand)
	meta := toolMetaRows(n, width, verbose || expand)

	header := glyph + " " + term.Arg(name)
	// The summary is the FALLBACK, not the headline: when the argument block
	// is drawn it already carries the command, on its own line, in full. Two
	// copies of the same string is what pushed the duration off the right of
	// the screen — 65% of the owner's tool headers lost it at width 80 — so
	// the header keeps the one thing that must never be lost.
	if n.Summary != "" && len(args) == 0 {
		header = header + " " + term.Dim(truncCols(n.Summary, toolSummaryCap))
	}
	if n.StartedAt != 0 {
		header += " " + term.Dim("["+toolDuration(n, time.Now())+"]")
	}
	// The header closes with a rule to the right margin, which is what makes
	// the block read as one object rather than a stack of unrelated rows.
	if fill := width - term.VisibleLen(header) - 1; fill > 0 {
		header += " " + term.Dim(strings.Repeat("─", fill))
	}

	rows := make([]string, 1, 8+len(args)+len(meta)+max(bashCap, 0))
	rows[0] = header
	rows = append(rows, args...)
	rows = append(rows, meta...)

	if strings.TrimSpace(n.Output) != "" {
		// Tool stdout is the most likely vector for terminal-state
		// escapes that could break the painter (alt-screen, cursor
		// visibility, line wrap, mouse modes, OSC). Sanitize before
		// rendering so a wayward bubbletea / huh / less / etc. can
		// never bleed its escapes into the host terminal.
		output := strings.TrimRight(n.Output, "\n")
		safe := render.SanitizeForTerminal(output)
		shown, total := tailOutput(safe, bashCap)
		lines := strings.Split(shown, "\n")
		// A rule between the call and its result, so the eye can see where one
		// stops and the other begins. Only when there is something to divide.
		if len(rows) > 1 {
			rows = append(rows, blockRule(width))
		}
		if bashCap >= 0 && total > bashCap {
			rows = append(rows, blockRow(width, term.Dim(fmt.Sprintf("… last %d of %d lines", bashCap, total))))
		}
		dimGutter := term.Dim(quoteGutter) // hoisted: one styled gutter, not one per line
		for _, l := range lines {
			// CELLS, not bytes: this said width-len(gutter), and len("  │ ") is
			// SIX for a FOUR-column gutter (the rule is a three-byte rune), so
			// every tool-output row was trimmed two columns narrower than it had
			// room for. Same gutter, same constant, one place — thinking and tool
			// output cannot drift apart again.
			rows = append(rows, dimGutter+truncCols(l, blockWidth(width)))
		}
	}
	return rows
}

// Two folds, because a value that is still arriving and a value that has
// settled are different objects to a reader.
//
//   - STREAMING keeps the LAST argStreamLines rows: a moving window on what is
//     being typed, and five is enough to see a paragraph take shape rather
//     than a single line twitching.
//   - SETTLED keeps the FIRST argSettledLines: nothing is moving any more, so
//     the useful thing is the head — what this call is — not the tail, which
//     is where it happened to stop.
//
// Expanded (Enter on the selection, or Ctrl-O) lifts both.
const argStreamLines = 5
const argSettledLines = 2

// argGutter marks argument rows. It is the SAME rule the output block draws —
// a dotted variant read as a different kind of object rather than the same
// object in a different voice — and the distinction is carried by colour
// instead: the rule and the text are drawn in term.Arg, against output's dim.
const argGutter = quoteGutter
const argGutterCells = quoteGutterCells

// argLabelCap bounds the shared label column so one long argument name cannot
// push every value off the right of the screen.
const argLabelCap = 12

// toolArgRows draws a tool's arguments under the block rule: one label per
// field, its value beside it when it is short and beneath it when it is not.
// The same shape whether the value is still arriving (Input, a truncated JSON
// prefix walked by partialjson) or settled (Args, decoded) — a node must not
// change layout under the reader because the model finished typing.
//
// A folded multi-line value says so on its LABEL row — `content (…last 5 of
// 41 lines)` — rather than in a row of its own. The count moves as the value
// grows, which is information while it is arriving and a fact afterwards; what
// it must not do is cost a row, because rows are what is being rationed.
//
// A multi-line value is followed by a blank rule row, so the next argument
// starts clear of it. A single-line value is not: nothing has been separated.
func toolArgRows(n livedoc.Node, width int, expand bool) []string {
	fields := toolArgFields(n)
	if len(fields) == 0 {
		return nil
	}
	streaming := strings.TrimSpace(n.Input) != ""
	avail := blockWidth(width)
	label := 0
	for _, f := range fields {
		if n := runewidth.StringWidth(f.Name); n > label && n <= argLabelCap {
			label = n
		}
	}
	var rows []string
	for _, f := range fields {
		value := strings.TrimRight(render.SanitizeForTerminal(f.Value), "\n")
		// A short one-line value rides beside its label; anything longer gets
		// the label to itself and the value indented beneath, where a wrapped
		// remainder cannot be read as the next argument.
		if !strings.Contains(value, "\n") && runewidth.StringWidth(value) <= avail-label-1 {
			row := blockRow(width, term.Label(runewidth.FillRight(f.Name, label)))
			if value != "" {
				row += " " + term.Arg(value)
			}
			rows = append(rows, row)
			continue
		}
		lines := hardWrap(value, avail-2)
		kept := foldArgLines(lines, expand, streaming)
		head := f.Name
		if len(kept) < len(lines) {
			head += " " + argFoldNote(len(kept), len(lines), streaming)
		}
		rows = append(rows, blockRow(width, term.Label(head)))
		for _, l := range kept {
			rows = append(rows, blockRow(width, "  "+term.Arg(truncCols(l, avail-2))))
		}
		rows = append(rows, blockRow(width, "")) // a multiline value closes with air
	}
	return rows
}

// foldArgLines applies the fold: the LAST argStreamLines rows while the value
// is arriving, the FIRST argSettledLines (blank rows skipped) once it has
// stopped, everything when expanded. tailOutput is the same helper the output
// block folds with, so the two cannot drift.
func foldArgLines(lines []string, expand, streaming bool) []string {
	if expand {
		return lines
	}
	switch {
	case streaming && len(lines) > argStreamLines:
		shown, _ := tailOutput(strings.Join(lines, "\n"), argStreamLines)
		return strings.Split(shown, "\n")
	case !streaming:
		kept := make([]string, 0, argSettledLines)
		for _, l := range lines {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if kept = append(kept, l); len(kept) == argSettledLines {
				break
			}
		}
		return kept
	}
	return lines
}

// argFoldNote is the label's suffix when a value is folded. It says which END
// was kept, because that differs by phase and the reader cannot otherwise tell
// whether they are looking at the beginning of a command or the end of one.
func argFoldNote(shown, total int, streaming bool) string {
	end := "first"
	if streaming {
		end = "last"
	}
	return fmt.Sprintf("(…%s %d of %d lines)", end, shown, total)
}

// blockWidth is the room a block row has for content: the pane, less the
// gutter, less ONE column of right padding. The padding is there because a row
// rendered flush to the last column has been seen to spill a character — wide
// runes and the selection bar each cost a cell the arithmetic did not always
// account for — and a column of air is cheaper than a wrapped glyph.
func blockWidth(width int) int {
	w := width - quoteGutterCells - 1
	if w < 8 {
		w = 8
	}
	return w
}

// blockRow puts one row inside the tool block, under the SAME rule the output
// uses. The rule is identical in glyph and colour on both sides of the block:
// what tells an argument from output is the colour of the TEXT, not the
// furniture around it.
//
// It CLIPS, and that is the point: every row in the block goes through here,
// so no row can be wider than its pane. The label row was the one that got
// away — a fold note like `command (…first 2 of 39 lines)` is longer than a
// narrow pane and nothing was trimming it, which is the "one character past
// the wrap" the owner reported.
func blockRow(width int, content string) string {
	return term.Dim(quoteGutter) + clipToWidth(content, blockWidth(width))
}

// blockRule is the horizontal divider drawn between the parts of a block — in
// the header, and between the arguments and the output — so the eye can see
// where the call stops and its result begins.
func blockRule(width int) string {
	n := blockWidth(width)
	if n < 1 {
		return term.Dim(quoteGutter)
	}
	// Flush against the bar — `  │────`, not `  │ ────` — so the divider reads
	// as part of the block's frame rather than as a row of content in it.
	return term.Dim(strings.TrimRight(quoteGutter, " ") + strings.Repeat("─", n+1))
}

// toolArgFields is the one place that answers "what are this tool's
// arguments" — the streaming prefix while it arrives, the decoded map once it
// lands. No tool name is consulted in either case.
func toolArgFields(n livedoc.Node) []partialjson.Field {
	if strings.TrimSpace(n.Input) != "" {
		f := partialjson.Fields([]byte(n.Input))
		// By name, in BOTH phases. The streamed order is the model's (arrival)
		// and the settled order is a Go map's (arbitrary), so anything else
		// reshuffles the block the instant the arguments land — a node
		// changing shape under the reader for no reason they can see.
		sort.Slice(f, func(i, j int) bool { return f[i].Name < f[j].Name })
		return f
	}
	if len(n.Args) == 0 {
		return nil
	}
	keys := make([]string, 0, len(n.Args))
	for k := range n.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]partialjson.Field, 0, len(keys))
	for _, k := range keys {
		v, ok := n.Args[k].(string)
		if !ok {
			v = fmt.Sprintf("%v", n.Args[k])
		}
		out = append(out, partialjson.Field{Name: k, Value: v, Done: true})
	}
	return out
}

// tailRows hard-wraps text to w columns and keeps the LAST limit rows, which
// is what a still-growing value wants: the newest bytes are the interesting
// ones, and the region stays bounded however large the argument gets.
func tailRows(text string, w, limit int) []string {
	rows := hardWrap(strings.TrimRight(text, "\n"), w)
	if limit >= 0 && len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	return rows
}

func tailOutput(output string, limit int) (string, int) {
	total := 1 + strings.Count(output, "\n")
	if limit < 0 || total <= limit {
		return output, total
	}
	if limit == 0 {
		return "", total
	}
	at := len(output)
	for range limit {
		at = strings.LastIndexByte(output[:at], '\n')
		if at < 0 {
			return output, total
		}
	}
	return output[at+1:], total
}

func toolDuration(n livedoc.Node, now time.Time) string {
	end := n.FinishedAt
	if end == 0 {
		end = now.UnixMilli()
	}
	d := time.Duration(end-n.StartedAt) * time.Millisecond
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func formatToolTime(ms int64) string {
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05.000 MST")
}

// toolMetaRows draws the call's metadata inside the block. It is what Ctrl-O
// shows and what a selection shows, and it is deliberately the ONLY thing
// Ctrl-O adds: verbosity is metadata, not content. Drawn under the same rule
// as everything else in the block — before this they hung off the left margin
// in their own indentation, which read as though they belonged to the turn
// rather than to the call.
func toolMetaRows(n livedoc.Node, width int, show bool) []string {
	if !show || n.StartedAt == 0 {
		return nil
	}
	rows := []string{blockRow(width, term.Label("started ")+term.Arg(formatToolTime(n.StartedAt)))}
	if n.FinishedAt != 0 {
		rows = append(rows, blockRow(width, term.Label("finished ")+term.Arg(formatToolTime(n.FinishedAt))))
	}
	return rows
}

// hardWrap char-wraps s (runewidth-aware) to at most w columns per line,
// preserving explicit newlines.
func hardWrap(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	var rows []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			rows = append(rows, "")
			continue
		}
		col := 0
		var b strings.Builder
		for _, r := range para {
			rw := runewidth.RuneWidth(r)
			if col+rw > w {
				rows = append(rows, b.String())
				b.Reset()
				col = 0
			}
			b.WriteRune(r)
			col += rw
		}
		rows = append(rows, b.String())
	}
	return rows
}

// truncCols truncates s to at most w display columns (runewidth-aware).
func truncCols(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return runewidth.Truncate(s, w, "")
}
