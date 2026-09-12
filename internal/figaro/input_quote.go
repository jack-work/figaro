package figaro

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/quote"
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

func (quoteRewrite) Rewrite(_ context.Context, view InputView, text string) (string, error) {
	r, rest, err := quote.Parse(text)
	if errors.Is(err, quote.ErrNone) {
		return text, nil
	}
	if err != nil {
		// quote.Parse says "quote: <token>: why"; the list adds its own
		// name, so hand back the part after it.
		return "", errors.New(strings.TrimPrefix(err.Error(), "quote: "))
	}
	passage, err := resolveQuote(view.Log, r)
	if err != nil {
		// The refusal names the token it refuses, so a reader who typed two
		// can tell which.
		return "", fmt.Errorf("<%s>: %w", text[1:strings.IndexByte(text, '>')], err)
	}
	return renderQuote(view, r, passage) + rest, nil
}

// resolveQuote is LT -> message -> block -> rune slice, with a refusal at
// every step that names what it found instead.
func resolveQuote(log LogReader, r quote.Range) (string, error) {
	if log == nil {
		return "", errors.New("no log to resolve against")
	}
	if !r.Offsets {
		msg, err := quoteMessage(log, r.Start.LT)
		if err != nil {
			return "", err
		}
		if !r.Block {
			return wholeMessageText(msg, r.Start.LT)
		}
		return blockText(msg, r.Start.LT, r.Start.Block)
	}
	if !r.Span() {
		msg, err := quoteMessage(log, r.Start.LT)
		if err != nil {
			return "", err
		}
		s, err := blockText(msg, r.Start.LT, r.Start.Block)
		if err != nil {
			return "", err
		}
		return runeSlice(s, r.Start, r.End.Offset)
	}
	// A span: the head of the first block from its offset, every whole block
	// between, and the tail of the last block to its offset.
	first, err := quoteMessage(log, r.Start.LT)
	if err != nil {
		return "", err
	}
	head, err := blockText(first, r.Start.LT, r.Start.Block)
	if err != nil {
		return "", err
	}
	head, err = runeSlice(head, r.Start, -1)
	if err != nil {
		return "", err
	}
	last, err := quoteMessage(log, r.End.LT)
	if err != nil {
		return "", err
	}
	tail, err := blockText(last, r.End.LT, r.End.Block)
	if err != nil {
		return "", err
	}
	tail, err = runeSlice(tail, quote.Coord{LT: r.End.LT, Block: r.End.Block}, r.End.Offset)
	if err != nil {
		return "", err
	}
	var parts []string
	parts = append(parts, head)
	// Whole blocks between the two ends, walking messages by LT. Blocks
	// without text (a tool call) are skipped rather than refused: the reader
	// selected across them, they did not name them.
	for lt := r.Start.LT; lt <= r.End.LT; lt++ {
		msg, ok := log.Lookup(lt)
		if !ok {
			continue
		}
		for i, c := range msg.Payload.Content {
			if lt == r.Start.LT && i <= r.Start.Block {
				continue
			}
			if lt == r.End.LT && i >= r.End.Block {
				continue
			}
			if hasText(c) {
				parts = append(parts, c.Text)
			}
		}
	}
	parts = append(parts, tail)
	return strings.Join(parts, "\n\n"), nil
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

// wholeMessageText joins every text block of a message.
func wholeMessageText(msg message.Message, lt uint64) (string, error) {
	var parts []string
	for _, c := range msg.Content {
		if hasText(c) {
			parts = append(parts, c.Text)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("lt %d has no text", lt)
	}
	return strings.Join(parts, "\n\n"), nil
}

// runeSlice is s[start:end) in runes; end < 0 means to the end. Offsets are
// checked against the block's rune count, and the refusal says the count.
func runeSlice(s string, at quote.Coord, end int) (string, error) {
	rs := []rune(s)
	n := len(rs)
	if end < 0 {
		end = n
	}
	if at.Offset > n || end > n {
		return "", fmt.Errorf("block %d of lt %d has %d chars, not %d-%d", at.Block, at.LT, n, at.Offset, end)
	}
	return string(rs[at.Offset:end]), nil
}

// renderQuote is the block the agent receives: a header naming the
// coordinate, the passage under a gutter, head and tail with an ellipsis
// between when it is long, then a blank line for the reader's own text.
func renderQuote(view InputView, r quote.Range, passage string) string {
	head, tail := 480, 160
	ellipsis, gutter, header := "…", "> ", true
	if l := view.Settings; l != nil {
		head, tail = l.QuoteHeadChars(), l.QuoteTailChars()
		ellipsis, gutter, header = l.QuoteEllipsis(), l.QuoteGutter(), l.QuoteHeader()
	}
	total := utf8.RuneCountInString(passage)
	body := passage
	if head+tail > 0 && total > head+tail {
		rs := []rune(passage)
		body = string(rs[:head]) + ellipsis + string(rs[total-tail:])
	}
	var b strings.Builder
	if header {
		b.WriteString(gutter)
		b.WriteString(quoteHeader(view, r, total))
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
