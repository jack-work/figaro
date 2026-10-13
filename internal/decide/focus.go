package decide

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/internal/store"
)

func focusText(log store.Log[message.Message], r quote.Range) (string, error) {
	if log == nil {
		return "", fmt.Errorf("%s names a passage, and there is no aria to read it from", quote.Format(r))
	}
	first, err := lookup(log, r.Start.LT)
	if err != nil {
		return "", err
	}
	switch {
	case !r.Block && !r.Offsets:
		text := messageText(first)
		if text == "" {
			return "", fmt.Errorf("lt %d has no text", r.Start.LT)
		}
		return text, nil
	case !r.Offsets:
		return blockText(first, r.Start)
	case !r.Span():
		text, err := blockText(first, r.Start)
		if err != nil {
			return "", err
		}
		return runes(text, r.Start, r.End.Offset)
	}
	head, err := blockText(first, r.Start)
	if err != nil {
		return "", err
	}
	head, err = runes(head, r.Start, -1)
	if err != nil {
		return "", err
	}
	last, err := lookup(log, r.End.LT)
	if err != nil {
		return "", err
	}
	tail, err := blockText(last, quote.Coord{LT: r.End.LT, Block: r.End.Block})
	if err != nil {
		return "", err
	}
	tail, err = runes(tail, quote.Coord{LT: r.End.LT, Block: r.End.Block}, r.End.Offset)
	if err != nil {
		return "", err
	}
	parts := []string{head}
	for lt := r.Start.LT; lt <= r.End.LT; lt++ {
		e, ok := log.Lookup(lt)
		if !ok {
			continue
		}
		for i, blk := range e.Payload.Content {
			if (lt == r.Start.LT && i <= r.Start.Block) || (lt == r.End.LT && i >= r.End.Block) {
				continue
			}
			if hasText(blk) {
				parts = append(parts, blk.Text)
			}
		}
	}
	parts = append(parts, tail)
	return strings.Join(parts, "\n"), nil
}

func lookup(log store.Log[message.Message], lt uint64) (message.Message, error) {
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

func messageText(m message.Message) string {
	var parts []string
	for _, c := range m.Content {
		if hasText(c) && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func blockText(m message.Message, at quote.Coord) (string, error) {
	if at.Block >= len(m.Content) {
		return "", fmt.Errorf("lt %d has %d blocks, not block %d", at.LT, len(m.Content), at.Block)
	}
	c := m.Content[at.Block]
	if !hasText(c) {
		return "", fmt.Errorf("block %d of lt %d has no text", at.Block, at.LT)
	}
	return c.Text, nil
}

func runes(s string, at quote.Coord, end int) (string, error) {
	n := utf8.RuneCountInString(s)
	if end < 0 {
		end = n
	}
	if at.Offset > n || end > n || at.Offset > end {
		return "", fmt.Errorf("block %d of lt %d has %d chars, not %d-%d", at.Block, at.LT, n, at.Offset, end)
	}
	r := []rune(s)
	return string(r[at.Offset:end]), nil
}
