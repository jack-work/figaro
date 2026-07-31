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
	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
)

const (
	nodeBashCapDefault  = 10
	nodeOutputUnlimited = -1
	toolSummaryCap      = 80 // default truncation for a tool's summary line

	// proseTableCapDefault is the collapsed height of ONE rendered markdown
	// table, in physical rows including its header and rule. It is prose's
	// answer to nodeBashCapDefault and it exists for the same reason: wrapping
	// a table's cells (which is what makes it readable at all — see
	// internal/render) also makes it TALL, and a transcript of tall tables is
	// its own kind of unreadable. A 2-row table costs 4 rows at 80 columns, 7
	// at 40 and 11 at 26, so this cap does not bite at any ordinary width; it
	// catches the long table on the narrow pane.
	//
	// Set it to proseTableUncapped to render every table in full always, which
	// makes prose permanently unexpandable. That is the one-constant switch for
	// the taste call.
	proseTableCapDefault = 12
	proseTableUncapped   = -1
)

// renderSettings is the consumer-side verbosity toggle. The wire/IR always
// carries the full data; this only affects display, so it can be flipped live
// (Ctrl-O) and the unit re-rendered. Thinking blocks are always shown (muted);
// verbose additionally expands tool inputs to the full wrapped command.
type renderSettings struct {
	verbose  bool
	jsonMode bool // -j / --json: emit a single {aria_id, ...} JSON line on stdout instead of a live render
	listen   bool // -l / --listen: auto-enter the transcript at startup
}

// nodeExpandable — THE SEAM between the gesture and the renderer — lives below,
// beside the clamp it consults. feat/mouse-nodes shipped a placeholder here
// (tools only, the behaviour toggleSelectedTools had inline); feat/table-wrap
// replaced it with the real predicate, which also answers for prose whose
// collapsed render drops rows. The merge kept BOTH copies without a textual
// conflict — a reminder that "auto-merging <file>" is not a semantic claim.

// renderNode draws ONE node. This is the single dispatch on node kind, shared
// by every view: `show` (via renderNodeList), the inline incipit and the
// transcript pager (via ariaView). It used to exist twice — ariaView switched
// on n.Type alone and so drew a turn's prompt as agent prose, putting the
// user's own question under the "< figaro" header while `show` correctly
// marked it "↳ input". Two renderers for one representation is the exact defect
// class turn addressing exists to remove; there is now one.
//
// expanded is the node's expansion state, made EXPLICIT. bashCap already
// carried it for tools (nodeBashCapDefault vs nodeOutputUnlimited) and nothing
// else needed it; prose needs it too, so it stops being smuggled in a cap and
// becomes a parameter. Callers that have no expansion model — `show`, the
// incipit — pass their verbose flag, which is the toggle the user already has
// there (see renderNodeList).
func renderNode(n livedoc.Node, width, bashCap int, tick uint64, verbose, expanded bool) []string {
	switch {
	case n.Type == livedoc.NodeTool:
		return renderToolNode(n, width, bashCap, tick, verbose)
	case n.Type == livedoc.NodeThinking:
		return renderThinkingNode(n, width, expanded)
	// Steering is the only input-voice NODE there is — the inquiry is text on
	// the turn and never reaches here — so this marker cannot be mistaken for
	// the question that opened the turn.
	case n.Type == livedoc.NodeSteering:
		return renderSteeringNode(n, width, expanded)
	default:
		return renderProseNode(n, width, expanded)
	}
}

// nodeExpandable reports whether a node has a collapsed form — i.e. whether
// toggling its expansion would reveal anything its collapsed render does not
// already show. It is the predicate a gesture asks before it acts, so that a
// click on something with nothing to reveal is inert rather than a silent flag
// flip.
//
// The two kinds answer differently, on purpose:
//
//   - A TOOL is expandable when it has output. This is deliberately the same
//     test the tool-only toggle used before there was anything else to expand,
//     so generalizing the gesture cannot change what a click on a tool does.
//     (A stricter "and the output actually exceeds the cap" would be more
//     honest to the name and is a one-line change, but it is a behaviour change
//     and not this function's to make.)
//   - PROSE is expandable when its collapsed render genuinely drops rows —
//     which today means it contains a table taller than proseTableCapDefault.
//     Ordinary prose is never expandable, at any width.
//
// Cheap enough to call per gesture: render.Prose is memoized on
// (markdown, width) and the caller is about to render the node anyway.
func nodeExpandable(n livedoc.Node, width int) bool {
	if n.Type == livedoc.NodeTool {
		return strings.TrimSpace(n.Output) != ""
	}
	if strings.TrimSpace(n.Markdown) == "" {
		return false
	}
	if width <= 0 {
		width = 80
	}
	rows := render.Prose(nodeMarkdown(n), proseWidth(n, width))
	return len(clampTables(rows, proseTableCapDefault, proseWidth(n, width))) != len(rows)
}

// renderTurnRows renders a whole exchange — the inquiry that opened the turn,
// then what the agent made of it — each under its own run header, with the rule
// between them. The inquiry is TEXT ON THE TURN, so it is drawn from
// Turn.Inquiry; no renderer looks for it in the node list, because it is not
// there.
func renderTurnRows(inquiry string, segments []aria.InquirySegment, nodes []livedoc.Node, width, bashCap int, tick uint64, set renderSettings) []string {
	if width <= 0 {
		width = 80
	}
	var rows []string
	if iq := inquiryRowsFor(inquiry, segments, width); len(iq) > 0 {
		rows = append(rows, messageHeader(livedoc.RoleInput), "")
		for _, l := range iq {
			rows = append(rows, clipToWidth(l, width))
		}
	}
	body := renderNodeList(nodes, width, bashCap, tick, set)
	if len(body) == 0 {
		return rows
	}
	if len(rows) > 0 {
		// The question closes with a blank and the RULE before the agent speaks.
		// It used to get that rule for free, as the closer of its own message;
		// now the two voices share one message and the rule is drawn here.
		rows = append(rows, "", dimTransRule(width))
	}
	if h := messageHeader(livedoc.RoleOutput); h != "" {
		rows = append(rows, h, "")
	}
	return append(rows, body...)
}

// proseIndent matches the inset render.Prose gives a paragraph, so metadata
// drawn beside prose lines up with it instead of floating left of it.
const proseIndent = "  "

// inquiryRowsFor draws a turn's opening question, attributed when it can be.
//
// ONE "> input" header for the whole question however many people wrote it —
// they are one message, and a header per submission would say otherwise. Each
// segment is then prefaced by its sender in the dim register used for block
// timestamps and tool durations, with a blank line between segments so the
// parts read as separate messages rather than one paragraph.
//
// An UNKNOWN sender draws NOTHING — not "unknown", not a blank row. Most
// messages ever written carry no sender, and a placeholder on each of them
// would be noise where there used to be none. With no attribution at all the
// output is exactly what it was before senders existed.
func inquiryRowsFor(inquiry string, segments []aria.InquirySegment, width int) []string {
	if len(segments) == 0 {
		return inquiryProse(inquiry, width)
	}
	var rows []string
	for i, seg := range segments {
		if i > 0 {
			rows = append(rows, "")
		}
		if seg.Sender != "" {
			// Indented to sit under the prose, which render.Prose insets. A
			// flush-left attribution over an inset paragraph reads as a
			// heading rather than as a label on the text below it.
			rows = append(rows, clipToWidth(term.Dim(proseIndent+seg.Sender), width))
		}
		rows = append(rows, inquiryProse(seg.Text, width)...)
	}
	return rows
}

// inquiryProse wraps a turn's opening question to the same prose renderer its
// nodes use, so the question looks exactly as it did when it was still a node.
// The caller supplies the "> input" header, because each view decorates rows
// its own way. Empty inquiry, no rows.
//
// Deliberately NOT table-clamped: a node is the agent's output and may be
// summarised, but the question is the user's own text and figaro does not
// truncate what the user wrote.
func inquiryProse(inquiry string, width int) []string {
	if strings.TrimSpace(inquiry) == "" {
		return nil
	}
	if width <= 0 {
		width = 80
	}
	return render.Prose(inquiry, width)
}

// renderNodeList renders a unit's whole node list to terminal rows. The list
// is walked uniformly — every tool renders through renderToolNode with no
// per-tool branching. One blank row separates adjacent blocks; a final
// clipToWidth pass keeps every row on a single physical line.
func renderNodeList(nodes []livedoc.Node, width, bashCap int, tick uint64, set renderSettings) []string {
	if width <= 0 {
		width = 80
	}
	if bashCap <= 0 {
		bashCap = nodeBashCapDefault
	}
	var rows []string
	for i, n := range nodes {
		// Empty prose/thinking nodes are minted by the projection so ids cannot
		// shift when a block fills (docs/turn-addressing.md, invariant 6).
		// Hiding them is the renderer's job, not the projection's.
		if n.Type != livedoc.NodeTool && strings.TrimSpace(n.Markdown) == "" {
			continue
		}
		var nr []string
		// expanded: true, unconditionally. This is `show`'s path (and the row
		// counters'), and `show` is a one-shot dump the reader scrolls in their own
		// terminal — there is no viewport to husband and no gesture to un-collapse
		// with, so hiding table rows here would help nobody and could not be
		// undone. Only the transcript collapses; see ariaView.Render.
		nr = renderNode(n, width, bashCap, tick, set.verbose, true)
		// Under the verbose toggle every node reports when it was written, the
		// same way a tool reports started/finished. Tools already print their own
		// richer timing, so they are left alone.
		if set.verbose && n.At != 0 && n.Type != livedoc.NodeTool {
			nr = append(nr, term.Dim("  "+formatToolTime(n.At)))
		}
		if i > 0 {
			nr = append([]string{""}, nr...)
		}
		rows = append(rows, nr...)
	}
	for i := range rows {
		rows[i] = clipToWidth(rows[i], width)
	}
	return rows
}

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
	ascii := true
	for j := i + 1; j < len(s); j++ {
		c := s[j]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			return j + 1, ascii
		}
		if c >= utf8.RuneSelf {
			ascii = false
		}
	}
	return len(s), ascii
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
	rs := []rune(s)
	clipped := false
	for i := 0; i < len(rs); {
		if rs[i] == '\x1b' { // copy the whole escape sequence, uncounted
			j := i + 1
			for j < len(rs) && !((rs[j] >= 'A' && rs[j] <= 'Z') || (rs[j] >= 'a' && rs[j] <= 'z')) {
				j++
			}
			if j < len(rs) {
				j++
			}
			b.WriteString(string(rs[i:j]))
			i = j
			continue
		}
		r := rs[i]
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
		i++
	}
	if clipped {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

func renderProseNode(n livedoc.Node, width int, expanded bool) []string {
	return nodeProseRows(n, width, expanded)
}

// renderThinkingNode renders extended-thinking text as a dim blockquote
// (glamour styles "> " spans), visually distinct from the agent's prose.
func renderThinkingNode(n livedoc.Node, width int, expanded bool) []string {
	return nodeProseRows(n, width, expanded)
}

// renderSteeringNode renders a user message injected mid-turn — a steering
// interjection — under a marked gutter so it reads as the user's voice inside
// the assistant's turn, distinct from prose and thinking.
func renderSteeringNode(n livedoc.Node, width int, expanded bool) []string {
	// Subdued relative to the inquiry, deliberately: steering nudges a train of
	// thought already in motion, it does not open a turn. The inquiry gets a run
	// header and full-strength prose; steering gets an inline marker and a dim
	// blockquote gutter, so it reads as an aside within the agent's stream.
	head := term.Dim("↳ input")
	if n.Sender != "" {
		head += " " + term.Dim("· "+n.Sender)
	}
	return append([]string{head}, nodeProseRows(n, width, expanded)...)
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
const quoteGutterCells = 4

// nodeProseRows renders a non-tool node's markdown, clamping over-tall tables
// unless the node is expanded. This is prose's whole collapsed form.
func nodeProseRows(n livedoc.Node, width int, expanded bool) []string {
	rows := render.Prose(nodeMarkdown(n), proseWidth(n, width))
	if !expanded {
		rows = clampTables(rows, proseTableCapDefault, proseWidth(n, width))
	}
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
	// So the width is reserved BEFORE rendering and the rule is prefixed after.
	// Nothing to detect, nothing to restore, nothing to clip: the defect is
	// unrepresentable rather than tested for. Tool output has always drawn its
	// own gutter this way.
	dim := term.Dim(quoteGutter) // one styled rule, not one per row
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, dim+r)
	}
	return out
}

// clampTables limits each rendered markdown table to cap physical rows,
// replacing the remainder with a dim count. It is the prose analogue of a
// tool's output cap and shares its idiom, with one deliberate difference: a
// tool keeps the TAIL of its output ("… last 10 of 42 lines") because the end
// of a command's output is the interesting part, while a table keeps its HEAD,
// because a table without its header row is not a table.
//
// Returns rows unchanged — same backing array, no allocation — when no table
// overruns, which is every ordinary width. That matters: this runs on every
// row-cache fill and every incipit repaint.
func clampTables(rows []string, cap, width int) []string {
	if cap <= 0 || len(rows) == 0 {
		return rows
	}
	spans := render.TableSpans(rows)
	over := false
	for _, s := range spans {
		if s[1]-s[0] > cap {
			over = true
			break
		}
	}
	if !over {
		return rows
	}
	out := make([]string, 0, len(rows))
	at := 0
	for _, s := range spans {
		out = append(out, rows[at:s[0]]...)
		if h := s[1] - s[0]; h > cap {
			out = append(out, rows[s[0]:s[0]+cap]...)
			// The note is content like any other row: it must fit the width the
			// rows were rendered at. It never had to before, because that width
			// was always the full one; now a quoted node reserves four columns for
			// its rule, and an unclipped note ran past the edge (caught by
			// TestClampTables_HoldsPainterInvariant at w=26, not by me).
			note := fmt.Sprintf("  … +%d more table lines", h-cap)
			if width > 0 {
				note = clipToWidth(note, width)
			}
			out = append(out, term.Dim(note))
		} else {
			out = append(out, rows[s[0]:s[1]]...)
		}
		at = s[1]
	}
	return append(out, rows[at:]...)
}

// renderToolNode draws a tool as a widget with ZERO per-tool control flow:
// a status glyph, the tool name, and — when set — the producer's Summary
// (truncated for the header, wrapped in verbose mode); then any streamed
// output under a dim gutter, tail-clamped to bashCap lines. In verbose mode
// Args are also rendered generically as sorted key=value lines. The client
// never inspects n.Name.
func renderToolNode(n livedoc.Node, width, bashCap int, tick uint64, expand bool) []string {
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
	header := glyph + " " + term.Cyan(name)
	if n.Summary != "" {
		header = header + " " + term.Dim(truncCols(n.Summary, toolSummaryCap))
	}
	if n.StartedAt != 0 {
		header += " " + term.Dim("["+toolDuration(n, time.Now())+"]")
	}
	// Header, optional arg/timestamp lines, then the tail-clamped output.
	rows := make([]string, 1, 6+max(bashCap, 0))
	rows[0] = header

	if expand && len(n.Args) > 0 {
		const g = "  "
		keys := make([]string, 0, len(n.Args))
		for k := range n.Args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			line := fmt.Sprintf("%s=%v", k, n.Args[k])
			for _, l := range hardWrap(line, width-len(g)) {
				rows = append(rows, term.Dim(g+l))
			}
		}
	}
	if expand && n.StartedAt != 0 {
		rows = append(rows, term.Dim("  started "+formatToolTime(n.StartedAt)))
		if n.FinishedAt != 0 {
			rows = append(rows, term.Dim("  finished "+formatToolTime(n.FinishedAt)))
		}
	}

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
		if bashCap >= 0 && total > bashCap {
			rows = append(rows, term.Dim(fmt.Sprintf("  │ … last %d of %d lines", bashCap, total)))
		}
		const gutter = "  │ "
		dimGutter := term.Dim(gutter) // hoisted: one styled gutter, not one per line
		for _, l := range lines {
			rows = append(rows, dimGutter+truncCols(l, width-len(gutter)))
		}
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

// blockquote prefixes each line of s with "> " (markdown blockquote).
func blockquote(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
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
