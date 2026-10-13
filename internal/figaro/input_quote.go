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

// quoteRewrite replaces a leading `<lt.block:start-end>!` with a short quote of
// the passage. It runs once, at the door; the log holds the resolved text.
type quoteRewrite struct{}

func (quoteRewrite) Name() string { return "quote" }

func (q quoteRewrite) Rewrite(ctx context.Context, view InputView, text string) (string, error) {
	r, rest, err := quote.Parse(text)
	if errors.Is(err, quote.ErrNone) {
		return text, nil
	}
	if err != nil {
		// quote.Parse's own prefix is stripped; the list adds its name.
		return "", errors.New(strings.TrimPrefix(err.Error(), "quote: "))
	}
	b, err := quoteBudget(view.Settings)
	if err != nil {
		return "", err
	}
	passage, err := resolveQuote(ctx, view.Log, r, b)
	if err != nil {
		return "", fmt.Errorf("<%s>: %w", text[1:strings.IndexByte(text, '>')], err)
	}
	return renderQuote(view, r, passage) + rest, nil
}

// budget bounds the runes copied from each end; the passage is walked, never materialized.
type budget struct{ head, tail int }

// quoteBudget refuses zero on both ends (an unbounded quote) and any negative.
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

// passage is the two ends the budget allowed and a count of the middle.
type passage struct {
	head, tail string
	total      int  // runes in the passage, whether or not they were copied
	truncated  bool // there is a middle, and it was dropped
}

// resolveQuote streams text through the clip a block at a time; the middle is never held.
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

// writeSpan walks the span a block at a time and stops at the end coordinate.
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
	// The last block is resolved first, so a bad end refuses before the walk.
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
	// Blocks without text (a tool call) are skipped, not refused: the reader selected across them.
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

// runeWindow is s[start:end) in runes, as a substring sharing the block's bytes. end < 0 means to the end.
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

func byteAt(s string, k int) int {
	i := 0
	for ; k > 0 && i < len(s); k-- {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return i
}

// lastRunesAt walks back from the end, so a long block is not scanned twice.
func lastRunesAt(s string, k int) int {
	i := len(s)
	for ; k > 0 && i > 0; k-- {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
	}
	return i
}

func byteFromEnd(b []byte, k int) int {
	i := len(b)
	for ; k > 0 && i > 0; k-- {
		_, size := utf8.DecodeLastRune(b[:i])
		i -= size
	}
	return i
}

// clip holds head+tail runes plus two counters; the passage is walked once.
type clip struct {
	budget
	headBuf   []byte
	headRunes int
	tailBuf   []byte
	tailRunes int
	total     int
	blocks    int
}

func (c *clip) write(s string) {
	if c.blocks > 0 {
		c.fold(blockSep)
	}
	c.blocks++
	c.fold(s)
}

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
		// This block alone fills the tail: keep its last runes, walking back from the end.
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

// When nothing was dropped the two ends overlap and the tail supplies what the head did not take.
func (c *clip) passage() passage {
	if c.total > c.head+c.tail {
		return passage{head: string(c.headBuf), tail: string(c.tailBuf), total: c.total, truncated: true}
	}
	rest := c.tailBuf[byteFromEnd(c.tailBuf, c.total-c.headRunes):]
	return passage{head: string(c.headBuf), tail: string(rest), total: c.total}
}

// renderQuote is the block the agent receives; see api/quote.Block.
func renderQuote(view InputView, r quote.Range, p passage) string {
	ellipsis, gutter, header := config.QuoteEllipsisDefault, config.QuoteGutterDefault, true
	if l := view.Settings; l != nil {
		ellipsis, gutter, header = l.QuoteEllipsis(), l.QuoteGutter(), l.QuoteHeader()
	}
	body := p.head + p.tail
	if p.truncated {
		body = p.head + ellipsis + p.tail
	}
	heading := ""
	if header {
		heading = quote.Heading(r, view.AriaID, turnOf(view, r), p.total)
	}
	return quote.Block(gutter, heading, body)
}

func turnOf(view InputView, r quote.Range) uint64 {
	if msg, ok := view.Log.Lookup(r.Start.LT); ok {
		return msg.Payload.TurnID
	}
	return 0
}
