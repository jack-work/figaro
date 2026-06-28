package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

const (
	altScreenOn  = "\x1b[?1049h"
	altScreenOff = "\x1b[?1049l"
)

// transcript is a full-screen, live-updating pager over the whole conversation,
// drawn on the alternate screen and toggled with Ctrl-T. Because it owns a fixed
// canvas with its own scroll buffer (over the in-memory aria model), it is both
// resize-clean and scrollable — the two things inline can't do at once. It reads
// the shared aria.Client, so it streams live: at the bottom it follows new
// output, otherwise it holds your scroll position.
//
// Keys: j/k line, u/d half-page, gg/G top/bottom, / literal search, q/Esc/Ctrl-T
// exit. Not safe for concurrent use; the caller serializes all entry points.
type transcript struct {
	out    io.Writer
	view   ldrender.NodeView
	client *aria.Client

	active bool
	w, h   int
	tick   int

	prev   []string // last painted screen (full-frame diff)
	offset int      // top line of the viewport into lines()
	follow bool     // stick to the bottom on new content
	pendG  bool     // saw one 'g' (for gg)

	inSearch bool
	query    string
}

func newTranscript(out io.Writer, w, h int, view ldrender.NodeView, client *aria.Client) *transcript {
	return &transcript{out: out, view: view, client: client, w: w, h: h}
}

// enter switches to the alternate screen and draws the transcript at the bottom.
func (t *transcript) enter() {
	t.active, t.follow, t.prev = true, true, nil
	t.pendG, t.inSearch, t.query = false, false, ""
	io.WriteString(t.out, altScreenOn+cursorHide+"\x1b[2J")
	t.render()
}

// leave restores the normal screen.
func (t *transcript) leave() {
	t.active = false
	io.WriteString(t.out, "\x1b[2J"+altScreenOff)
	t.prev = nil
}

func (t *transcript) resize(w, h int) {
	t.w, t.h = w, h
	t.prev = nil
	io.WriteString(t.out, "\x1b[2J")
	t.render()
}

// lines renders the whole conversation (committed messages + the open one) to
// physical rows, separated by a rule.
func (t *transcript) lines() []string {
	v := t.client.View()
	var out []string
	emit := func(m aria.Message) {
		for k, n := range m.Nodes {
			if k > 0 {
				out = append(out, "")
			}
			for _, l := range t.view.Render(n, t.w, t.tick) {
				out = append(out, clipToWidth(l, t.w))
			}
		}
		out = append(out, "", dimTransRule(t.w), "")
	}
	for _, m := range v.Closed {
		emit(m)
	}
	if v.Open != nil {
		emit(*v.Open)
	}
	return out
}

func (t *transcript) render() {
	if !t.active {
		return
	}
	all := t.lines()
	body := t.h - 1 // bottom row is the status bar
	if body < 1 {
		body = 1
	}
	maxOff := len(all) - body
	if maxOff < 0 {
		maxOff = 0
	}
	if t.follow {
		t.offset = maxOff
	}
	if t.offset > maxOff {
		t.offset = maxOff
	}
	if t.offset < 0 {
		t.offset = 0
	}
	screen := make([]string, t.h)
	for r := 0; r < body; r++ {
		if i := t.offset + r; i < len(all) {
			screen[r] = all[i]
		}
	}
	screen[t.h-1] = t.status(len(all))
	t.paint(screen)
}

func (t *transcript) status(total int) string {
	if t.inSearch {
		return "\x1b[7m" + clipToWidth("/"+t.query, t.w) + "\x1b[0m"
	}
	pos := "TOP"
	if total > t.h-1 {
		end := t.offset + t.h - 1
		if end > total {
			end = total
		}
		pos = fmt.Sprintf("%d-%d/%d", t.offset+1, end, total)
		if t.follow {
			pos += " (live)"
		}
	}
	s := fmt.Sprintf(" transcript  %s   j/k u/d  gg/G  / search  q exit ", pos)
	return "\x1b[7m" + clipToWidth(s, t.w) + "\x1b[0m"
}

func (t *transcript) paint(screen []string) {
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	for r := 0; r < len(screen); r++ {
		var old string
		if r < len(t.prev) {
			old = t.prev[r]
		}
		if screen[r] != old {
			fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", r+1, screen[r])
		}
	}
	b.WriteString("\x1b[?2026l")
	io.WriteString(t.out, b.String())
	t.prev = screen
}

// key handles one input byte; returns true when the transcript should close.
func (t *transcript) key(b byte) (done bool) {
	if t.inSearch {
		t.searchKey(b)
		t.render()
		return false
	}
	switch b {
	case 'q', 0x1b, 0x14: // q / Esc / Ctrl-T
		return true
	case 'j':
		t.offset++
		t.follow = false
	case 'k':
		t.offset--
		t.follow = false
	case 'd':
		t.offset += t.h / 2
		t.follow = false
	case 'u':
		t.offset -= t.h / 2
		t.follow = false
	case 'G':
		t.follow = true
	case 'g':
		if t.pendG {
			t.offset, t.follow = 0, false
		}
	case '/':
		t.inSearch, t.query = true, ""
	}
	t.pendG = b == 'g' && !t.pendG
	t.render()
	return false
}

func (t *transcript) searchKey(b byte) {
	switch b {
	case 0x0d, 0x0a: // Enter → jump to first match
		t.inSearch = false
		t.find(t.query)
	case 0x1b: // Esc → cancel
		t.inSearch, t.query = false, ""
	case 0x7f, 0x08: // backspace
		if len(t.query) > 0 {
			t.query = t.query[:len(t.query)-1]
		}
	default:
		if b >= 0x20 && b < 0x7f {
			t.query += string(b)
		}
	}
}

// find scrolls to the first line at/after the cursor containing q (wrapping).
func (t *transcript) find(q string) {
	if q == "" {
		return
	}
	all := t.lines()
	if len(all) == 0 {
		return
	}
	for i := 0; i < len(all); i++ {
		idx := (t.offset + 1 + i) % len(all)
		if strings.Contains(all[idx], q) {
			t.offset, t.follow = idx, false
			return
		}
	}
}

func dimTransRule(w int) string {
	if w < 3 {
		w = 3
	}
	return "\x1b[2m" + strings.Repeat("─", w) + "\x1b[0m"
}
