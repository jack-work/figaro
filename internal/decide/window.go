package decide

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

const (
	pageSize  = 24
	headShare = 3
)

type line struct {
	lt   uint64
	text string
	cost int
}

func window(log store.Log[message.Message], budget int, centre, centreEnd uint64) []line {
	if log == nil || budget <= 0 {
		return nil
	}
	tail, ok := log.PeekTail()
	if !ok {
		return nil
	}
	perMessage := budget / headShare
	spent := 0
	var chosen []line
	take := func(e store.Entry[message.Message]) bool {
		text, cost := render(e.Payload, perMessage)
		if text == "" {
			return true
		}
		if spent+cost > budget {
			return false
		}
		spent += cost
		chosen = append(chosen, line{lt: e.LT, text: text, cost: cost})
		return true
	}
	if centre == 0 {
		left := newWalker(log, backward, tail.LT+1)
		for {
			e, ok := left.next()
			if !ok || !take(e) {
				break
			}
		}
		reverse(chosen)
		return chosen
	}
	if centreEnd < centre {
		centreEnd = centre
	}
	left := newWalker(log, backward, centreEnd+1)
	right := newWalker(log, forward, centreEnd)
	leftLive, rightLive := true, true
	for leftLive || rightLive {
		if leftLive {
			e, ok := left.next()
			if !ok || !take(e) {
				leftLive = false
			}
		}
		if rightLive {
			e, ok := right.next()
			if !ok || !take(e) {
				rightLive = false
			}
		}
	}
	sortByLT(chosen)
	return chosen
}

type direction int

const (
	backward direction = iota
	forward
)

type walker struct {
	log    store.Log[message.Message]
	dir    direction
	cursor uint64
	page   []store.Entry[message.Message]
	at     int
	done   bool
}

func newWalker(log store.Log[message.Message], dir direction, cursor uint64) *walker {
	return &walker{log: log, dir: dir, cursor: cursor}
}

func (w *walker) next() (store.Entry[message.Message], bool) {
	for {
		if w.at < len(w.page) {
			e := w.page[w.at]
			w.at++
			if skip(e.Payload) {
				continue
			}
			return e, true
		}
		if w.done || !w.fill() {
			return store.Entry[message.Message]{}, false
		}
	}
}

func (w *walker) fill() bool {
	var page []store.Entry[message.Message]
	switch w.dir {
	case backward:
		if w.cursor <= 1 {
			return false
		}
		page, _ = w.log.ReadPage(0, w.cursor, pageSize)
		if len(page) == 0 {
			return false
		}
		w.cursor = page[0].LT
		reverse(page)
	case forward:
		page, _ = w.log.ReadPage(w.cursor+1, 0, pageSize)
		if len(page) == 0 {
			return false
		}
		w.cursor = page[len(page)-1].LT
	}
	if len(page) < pageSize {
		w.done = true
	}
	w.page, w.at = page, 0
	return true
}

func skip(m message.Message) bool {
	return message.IsCeremonial(m) || m.Role == message.RoleGenesis || m.Role == message.RoleSystemInterrupt
}

func render(m message.Message, capTokens int) (string, int) {
	var parts []string
	for _, c := range m.Content {
		switch c.Type {
		case message.ContentProse:
			if trimmed(c.Text) != "" {
				parts = append(parts, c.Text)
			}
		case message.ContentToolInvoke:
			parts = append(parts, toolCall(c))
		case message.ContentToolResult:
			parts = append(parts, c.Text)
		case message.ContentImage:
			parts = append(parts, "[image]")
		}
	}
	text := trimmed(strings.Join(parts, "\n"))
	if text == "" {
		return "", 0
	}
	out := speaker(m) + ": " + clip(text, capTokens*bytesPerTokenNum/bytesPerTokenDen)
	return out, estimate(out)
}

func toolCall(c message.Content) string {
	if len(c.Arguments) == 0 {
		return c.ToolName
	}
	args, err := json.Marshal(c.Arguments)
	if err != nil {
		return c.ToolName
	}
	return fmt.Sprintf("%s %s", c.ToolName, args)
}

func speaker(m message.Message) string {
	switch m.Role {
	case message.RoleOutput:
		return "figaro"
	case message.RoleToolResult:
		return "tool"
	case message.RoleSystem:
		return "summary"
	default:
		return "you"
	}
}

func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func sortByLT(lines []line) {
	for i := 1; i < len(lines); i++ {
		for j := i; j > 0 && lines[j-1].lt > lines[j].lt; j-- {
			lines[j-1], lines[j] = lines[j], lines[j-1]
		}
	}
}
