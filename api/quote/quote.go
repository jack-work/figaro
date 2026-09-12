// Package quote is the grammar of a quoted coordinate: the token a client
// puts at the start of a prompt to say "this passage of the conversation".
//
//	<412.0:23-1180>!       block 0 of the message at lt 412, runes [23,1180)
//	<412.0:23-419.2:88>!   from rune 23 of 412.0 to rune 88 of 419.2
//	<412.0>!               all of block 0
//	<412>!                 all of the message
//
// The trailing '!' is the terminator, the same convention as an @key! form
// reference: without it the token is literal text and no rewrite touches it.
// Offsets are RUNE indices into the block's text, half open, so a coordinate
// can never land inside a multibyte character. The block index is not
// optional dressing: a message that says a paragraph and then calls a tool
// is one lt and two blocks.
//
// This package depends on nothing inside figaro so that a client and the
// daemon parse one grammar from one file.
package quote

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Coord is one end of a range: a message, a block of it, and a rune offset.
type Coord struct {
	LT    uint64
	Block int
	// Offset is a rune index into the block's text. It is meaningful only when
	// the Range that carries this Coord has Offsets set.
	Offset int
}

// Range is what a token names.
//
// Three shapes, decided by two flags:
//
//	Offsets  Block   meaning
//	false    false   the whole message at Start.LT
//	false    true    the whole block Start.LT.Start.Block
//	true     true    runes [Start.Offset, End.Offset) between the two coords
//
// When Offsets is set, End may name a different lt or block from Start (a
// span). When it is not, End equals Start.
type Range struct {
	Start, End Coord
	Block      bool // a block index was written
	Offsets    bool // offsets were written
}

// Span reports whether the range crosses message or block boundaries.
func (r Range) Span() bool {
	return r.Offsets && (r.Start.LT != r.End.LT || r.Start.Block != r.End.Block)
}

// Format renders r in the canonical spelling, terminator included, so that
// Parse(Format(r)) == r.
func Format(r Range) string {
	var b strings.Builder
	b.WriteByte('<')
	b.WriteString(strconv.FormatUint(r.Start.LT, 10))
	if r.Block {
		b.WriteByte('.')
		b.WriteString(strconv.Itoa(r.Start.Block))
	}
	if r.Offsets {
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(r.Start.Offset))
		b.WriteByte('-')
		if r.Span() {
			b.WriteString(strconv.FormatUint(r.End.LT, 10))
			b.WriteByte('.')
			b.WriteString(strconv.Itoa(r.End.Block))
			b.WriteByte(':')
		}
		b.WriteString(strconv.Itoa(r.End.Offset))
	}
	b.WriteString(">!")
	return b.String()
}

// ErrNone is returned by Parse when the text does not begin with a
// terminated token at all. It is not a malformed token: it is text.
var ErrNone = errors.New("no quote token")

// Parse reads one terminated token from the START of text and returns it
// with the rest of the text, leading whitespace trimmed.
//
// Three outcomes, and they are distinct on purpose:
//
//   - text does not start with '<', or the matching '>' is not followed by
//     '!': ErrNone. The caller leaves the text alone.
//   - it does, but what is between the brackets is not a coordinate: a
//     descriptive error. The caller refuses the message, because a reader
//     who typed the terminator meant a quote.
//   - a Range and the rest.
func Parse(text string) (Range, string, error) {
	if !strings.HasPrefix(text, "<") {
		return Range{}, text, ErrNone
	}
	end := strings.IndexByte(text, '>')
	if end < 0 || end+1 >= len(text) || text[end+1] != '!' {
		return Range{}, text, ErrNone
	}
	body := text[1:end]
	r, err := parseBody(body)
	if err != nil {
		return Range{}, text, fmt.Errorf("quote: <%s>: %w", body, err)
	}
	rest := strings.TrimLeft(text[end+2:], " \t\r\n")
	return r, rest, nil
}

func parseBody(body string) (Range, error) {
	if body == "" {
		return Range{}, errors.New("empty coordinate")
	}
	if strings.ContainsAny(body, " \t\r\n") {
		return Range{}, errors.New("a coordinate has no spaces")
	}
	var r Range
	// Split off the offsets first: "lt[.block]" [":" start "-" [lt.block ":"] end]
	head, tail, hasOffsets := strings.Cut(body, ":")
	lt, block, hasBlock, err := parseCoord(head)
	if err != nil {
		return Range{}, err
	}
	r.Start = Coord{LT: lt, Block: block}
	r.End = r.Start
	r.Block = hasBlock
	if !hasOffsets {
		return r, nil
	}
	if !hasBlock {
		return Range{}, errors.New("offsets need a block: write lt.block:start-end")
	}
	r.Offsets = true
	startS, endS, ok := strings.Cut(tail, "-")
	if !ok {
		return Range{}, errors.New("offsets are start-end")
	}
	start, err := parseOffset(startS, "start")
	if err != nil {
		return Range{}, err
	}
	r.Start.Offset = start
	// A span end carries its own coordinate: "lt.block:end".
	if endHead, endOff, spanned := strings.Cut(endS, ":"); spanned {
		elt, eblock, eHasBlock, err := parseCoord(endHead)
		if err != nil {
			return Range{}, fmt.Errorf("span end: %w", err)
		}
		if !eHasBlock {
			return Range{}, errors.New("span end needs a block: write lt.block:end")
		}
		r.End = Coord{LT: elt, Block: eblock}
		endS = endOff
	}
	end, err := parseOffset(endS, "end")
	if err != nil {
		return Range{}, err
	}
	r.End.Offset = end
	if !r.Span() && end < start {
		return Range{}, fmt.Errorf("end %d is before start %d", end, start)
	}
	if r.Span() && (r.End.LT < r.Start.LT || (r.End.LT == r.Start.LT && r.End.Block < r.Start.Block)) {
		return Range{}, errors.New("span end is before its start")
	}
	return r, nil
}

// parseCoord reads "lt" or "lt.block".
func parseCoord(s string) (lt uint64, block int, hasBlock bool, err error) {
	ltS, blockS, hasBlock := strings.Cut(s, ".")
	if ltS == "" {
		return 0, 0, false, errors.New("missing lt")
	}
	lt, err = strconv.ParseUint(ltS, 10, 64)
	if err != nil {
		return 0, 0, false, fmt.Errorf("lt %q is not a number", ltS)
	}
	if lt == 0 {
		return 0, 0, false, errors.New("lt 0 is not a message")
	}
	if !hasBlock {
		return lt, 0, false, nil
	}
	if blockS == "" {
		return 0, 0, false, errors.New("missing block after '.'")
	}
	block, err = strconv.Atoi(blockS)
	if err != nil || block < 0 {
		return 0, 0, false, fmt.Errorf("block %q is not a non-negative number", blockS)
	}
	return lt, block, true, nil
}

func parseOffset(s, which string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("missing %s offset", which)
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s offset %q is not a non-negative number", which, s)
	}
	return n, nil
}
