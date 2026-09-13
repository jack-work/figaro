package figaro

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/internal/config"
)

// quoteRewrite resolves a leading `<lt.block:start-end>!` against the log and
// replaces it with a short quote of the passage it names. The agent already
// holds the passage in its context; what it needs is enough to recognise it.
//
// The rewrite happens ONCE, at the door, and the log holds the resolved text.
// A replay, a re-read and a fork all see what the model saw, and the
// coordinate never has to be re-resolvable.
type quoteRewrite struct{}

func (quoteRewrite) Name() string { return "quote" }

func (q quoteRewrite) Rewrite(ctx context.Context, view InputView, text string) (string, error) {
	r, rest, err := quote.Parse(text)
	if errors.Is(err, quote.ErrNone) {
		return text, nil
	}
	if err != nil {
		// quote.Parse says "quote: <token>: why"; the list adds its own
		// name, so hand back the part after it.
		return "", errors.New(strings.TrimPrefix(err.Error(), "quote: "))
	}
	b, err := quoteBudget(view.Settings)
	if err != nil {
		return "", err
	}
	passage, err := resolveQuote(ctx, view.Log, r, b)
	if err != nil {
		// The refusal names the token it refuses, so a reader who typed two
		// can tell which.
		return "", fmt.Errorf("<%s>: %w", text[1:strings.IndexByte(text, '>')], err)
	}
	return renderQuote(view, r, passage) + rest, nil
}

// budget is how much of a passage the quote may COPY: runes from the front,
// runes from the back. It is the bound on everything below: the passage itself
// is walked, never materialized.
type budget struct{ head, tail int }

// quoteBudget reads [quote] and refuses a budget that cannot quote anything.
// Zero is a real value on either end (head=0 quotes only the tail), but zero on
// BOTH ends used to mean "send the whole passage", which is an unbounded
// default arrived at by accident. Negative is an error, not a clamp.
func quoteBudget(l *config.Loaded) (budget, error) {
	b := budget{head: config.QuoteHeadDefault, tail: config.QuoteTailDefault}
	if l != nil {
		b = budget{head: l.QuoteHeadChars(), tail: l.QuoteTailChars()}
	}
	switch {
	case b.head < 0 || b.tail < 0:
		return budget{}, fmt.Errorf("quote.head_chars and quote.tail_chars cannot be negative (have %d and %d)", b.head, b.tail)
	case b.head == 0 && b.tail == 0:
		return budget{}, errors.New("quote.head_chars and quote.tail_chars are both 0, so a quote would be empty: set one")
	}
	return b, nil
}

// passage is what the reader will see and how much was behind it: the two ends
// the budget allowed, and nothing of the middle.
type passage struct {
	head, tail string
	total      int  // runes in the passage, whether or not they were copied
	truncated  bool // there is a middle, and it was dropped
}

// resolveQuote is LT -> message -> block -> a window of runes, with a refusal
// at every step that names what it found instead. Text is streamed through the
// clip a block at a time and the middle is never held.
func resolveQuote(ctx context.Context, log LogReader, r quote.Range, b budget) (passage, error) {
	if log == nil {
		return passage{}, errors.New("no log to resolve against")
	}
	c := clip{budget: b}
	switch {
	case !r.Offsets && !r.Block: // a whole message
		msg, err := quoteMessage(log, r.Start.LT)
		if err != nil {
			return passage{}, err
		}
		if err := writeMessage(&c, msg, r.Start.LT); err != nil {
			return passage{}, err
		}
	case !r.Offsets: // one whole block
		msg, err := quoteMessage(log, r.Start.LT)
		if err != nil {
			return passage{}, err
		}
		text, err := blockText(msg, r.Start.LT, r.Start.Block)
		if err != nil {
			return passage{}, err
		}
		c.write(text)
	case !r.Span(): // one block, between two offsets
		msg, err := quoteMessage(log, r.Start.LT)
		if err != nil {
			return passage{}, err
		}
		text, err := blockText(msg, r.Start.LT, r.Start.Block)
		if err != nil {
			return passage{}, err
		}
		win, err := runeWindow(text, r.Start, r.End.Offset)
		if err != nil {
			return passage{}, err
		}
		c.write(win)
	default:
		if err := writeSpan(ctx, &c, log, r); err != nil {
			return passage{}, err
		}
	}
	return c.passage(), nil
}

// writeSpan walks the span a block at a time: the head of the first block from
// its offset, every text block between, and the tail of the last to its offset.
// It STOPS at the end coordinate, and the clip keeps only the two ends.
func writeSpan(ctx context.Context, c *clip, log LogReader, r quote.Range) error {
	first, err := quoteMessage(log, r.Start.LT)
	if err != nil {
		return err
	}
	head, err := blockText(first, r.Start.LT, r.Start.Block)
	if err != nil {
		return err
	}
	head, err = runeWindow(head, r.Start, -1)
	if err != nil {
		return err
	}
	// The last block is resolved FIRST, so a span that names a block with no
	// text refuses before a million intervening runes are walked.
	last, err := quoteMessage(log, r.End.LT)
	if err != nil {
		return err
	}
	tail, err := blockText(last, r.End.LT, r.End.Block)
	if err != nil {
		return err
	}
	tail, err = runeWindow(tail, quote.Coord{LT: r.End.LT, Block: r.End.Block}, r.End.Offset)
	if err != nil {
		return err
	}
	c.write(head)
	// Whole blocks between the two ends, walking messages by LT. Blocks
	// without text (a tool call) are skipped rather than refused: the reader
	// selected across them, they did not name them.
	for lt := r.Start.LT; lt <= r.End.LT; lt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		msg, ok := log.Lookup(lt)
		if !ok {
			continue
		}
		for i, blk := range msg.Payload.Content {
			if lt == r.Start.LT && i <= r.Start.Block {
				continue
			}
			if lt == r.End.LT && i >= r.End.Block {
				continue
			}
			if hasText(blk) {
				c.write(blk.Text)
			}
		}
	}
	c.write(tail)
	return nil
}

func quoteMessage(log LogReader, lt uint64) (message.Message, error) {
	e, ok := log.Lookup(lt)
	if !ok {
		return message.Message{}, fmt.Errorf("no message at lt %d", lt)
	}
	return e.Payload, nil
}

func hasText(c message.Content) bool {
	switch c.Type {
	case message.ContentProse, message.ContentThinking, message.ContentToolResult:
		return true
	}
	return false
}

func blockKind(c message.Content) string {
	switch c.Type {
	case message.ContentToolInvoke:
		return "a tool call"
	case message.ContentImage:
		return "an image"
	case message.ContentInterrupt:
		return "an interrupt"
	}
	return string(c.Type)
}

func blockText(msg message.Message, lt uint64, block int) (string, error) {
	n := len(msg.Content)
	if block >= n {
		return "", fmt.Errorf("lt %d has %s, not block %d", lt, blockCount(n), block)
	}
	c := msg.Content[block]
	if !hasText(c) {
		return "", fmt.Errorf("block %d of lt %d is %s and has no text", block, lt, blockKind(c))
	}
	return c.Text, nil
}

func blockCount(n int) string {
	if n == 1 {
		return "1 block"
	}
	return fmt.Sprintf("%d blocks", n)
}

// writeMessage streams every text block of a message through the clip.
func writeMessage(c *clip, msg message.Message, lt uint64) error {
	had := false
	for _, blk := range msg.Content {
		if hasText(blk) {
			c.write(blk.Text)
			had = true
		}
	}
	if !had {
		return fmt.Errorf("lt %d has no text", lt)
	}
	return nil
}

// runeWindow is s[start:end) counted in RUNES, returned as a substring: it
// shares the block's bytes and copies nothing. end < 0 means to the end.
// Offsets are checked against the block's rune count, and the refusal says the
// count.
func runeWindow(s string, at quote.Coord, end int) (string, error) {
	n := utf8.RuneCountInString(s)
	if end < 0 {
		end = n
	}
	if at.Offset > n || end > n || at.Offset > end {
		return "", fmt.Errorf("block %d of lt %d has %d chars, not %d-%d", at.Block, at.LT, n, at.Offset, end)
	}
	from := byteAt(s, at.Offset)
	return s[from : from+byteAt(s[from:], end-at.Offset)], nil
}

// byteAt is the byte index k runes into s.
func byteAt(s string, k int) int {
	i := 0
	for ; k > 0 && i < len(s); k-- {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return i
}

// lastRunesAt is the byte index where the last k runes of s begin. It walks
// back from the end, so a block far longer than the budget is not scanned
// twice.
func lastRunesAt(s string, k int) int {
	i := len(s)
	for ; k > 0 && i > 0; k-- {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
	}
	return i
}

// byteFromEnd is the byte index where the last k runes of b begin.
func byteFromEnd(b []byte, k int) int {
	i := len(b)
	for ; k > 0 && i > 0; k-- {
		_, size := utf8.DecodeLastRune(b[:i])
		i -= size
	}
	return i
}

// clip keeps the head and the tail of a passage and counts the rest. THE COST
// OF A QUOTE IS ITS OUTPUT: a four-megabyte selection is walked once, block by
// block, and what is held is head+tail runes plus two integers.
type clip struct {
	budget
	headBuf   []byte
	headRunes int
	tailBuf   []byte
	tailRunes int
	total     int
	blocks    int
}

// write folds one block (or one separator) into the two ends.
func (c *clip) write(s string) {
	if c.blocks > 0 {
		c.fold(blockSep)
	}
	c.blocks++
	c.fold(s)
}

// blockSep is what the reader sees between two blocks of one quote.
const blockSep = "\n\n"

func (c *clip) fold(s string) {
	n := utf8.RuneCountInString(s)
	c.total += n
	if c.headRunes < c.head {
		take := s
		want := c.head - c.headRunes
		if n > want {
			take = s[:byteAt(s, want)]
			c.headRunes = c.head
		} else {
			c.headRunes += n
		}
		c.headBuf = append(c.headBuf, take...)
	}
	if c.tail == 0 {
		return
	}
	if n >= c.tail {
		// This block alone fills the tail: drop what came before it and keep
		// its last runes, found by walking back from the end rather than
		// forward from the start. Nothing longer than the budget is copied.
		c.tailBuf = append(c.tailBuf[:0], s[lastRunesAt(s, c.tail):]...)
		c.tailRunes = c.tail
		return
	}
	c.tailBuf = append(c.tailBuf, s...)
	c.tailRunes += n
	if c.tailRunes > c.tail {
		off := byteFromEnd(c.tailBuf, c.tail)
		c.tailBuf = c.tailBuf[:copy(c.tailBuf, c.tailBuf[off:])]
		c.tailRunes = c.tail
	}
}

// passage assembles what was kept. When nothing was dropped the two ends
// overlap, and the tail supplies exactly the runes the head did not take.
func (c *clip) passage() passage {
	if c.total > c.head+c.tail {
		return passage{head: string(c.headBuf), tail: string(c.tailBuf), total: c.total, truncated: true}
	}
	rest := c.tailBuf[byteFromEnd(c.tailBuf, c.total-c.headRunes):]
	return passage{head: string(c.headBuf), tail: string(rest), total: c.total}
}

// renderQuote is the block the agent receives: a header naming the
// coordinate, the passage under a gutter, head and tail with an ellipsis
// between when it is long, then a blank line for the reader's own text.
func renderQuote(view InputView, r quote.Range, p passage) string {
	ellipsis, gutter, header := config.QuoteEllipsisDefault, config.QuoteGutterDefault, true
	if l := view.Settings; l != nil {
		ellipsis, gutter, header = l.QuoteEllipsis(), l.QuoteGutter(), l.QuoteHeader()
	}
	body := p.head + p.tail
	if p.truncated {
		body = p.head + ellipsis + p.tail
	}
	var b strings.Builder
	if header {
		b.WriteString(gutter)
		b.WriteString(quoteHeader(view, r, p.total))
		b.WriteByte('\n')
	}
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		b.WriteString(gutter)
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.String()
}

func quoteHeader(view InputView, r quote.Range, total int) string {
	var b strings.Builder
	b.WriteString("quoting")
	if view.AriaID != "" {
		b.WriteString(" aria " + view.AriaID)
	}
	if msg, ok := view.Log.Lookup(r.Start.LT); ok && msg.Payload.TurnID != 0 {
		fmt.Fprintf(&b, " · turn %d", msg.Payload.TurnID)
	}
	switch {
	case r.Span():
		fmt.Fprintf(&b, " · lt %d.%d:%d to %d.%d:%d", r.Start.LT, r.Start.Block, r.Start.Offset,
			r.End.LT, r.End.Block, r.End.Offset)
	case r.Offsets:
		fmt.Fprintf(&b, " · lt %d.%d · chars %d-%d", r.Start.LT, r.Start.Block, r.Start.Offset, r.End.Offset)
	case r.Block:
		fmt.Fprintf(&b, " · lt %d.%d", r.Start.LT, r.Start.Block)
	default:
		fmt.Fprintf(&b, " · lt %d", r.Start.LT)
	}
	fmt.Fprintf(&b, " (%d chars)", total)
	return b.String()
}
