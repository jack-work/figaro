package cli

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/render"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

// dimRunes reports, for each visible rune of a styled row, whether the
// terminal would draw it faint: SGR 2 turns faint on, 0 and 22 turn it off.
func dimRunes(row string) (plain []rune, dim []bool) {
	on := false
	for i := 0; i < len(row); {
		if row[i] == 0x1b {
			j := render.SkipEscape(row, i)
			seq := row[i:j]
			if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				params := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m")
				if params == "" {
					on = false
				}
				for _, p := range strings.Split(params, ";") {
					switch n, _ := strconv.Atoi(p); {
					case p == "" || n == 0 || n == 22:
						on = false
					case n == 2:
						on = true
					}
				}
			}
			i = j
			continue
		}
		r, size := []rune(row[i:])[0], len(string([]rune(row[i:])[0]))
		plain = append(plain, r)
		dim = append(dim, on)
		i += size
	}
	return plain, dim
}

func assertDimFrom(t *testing.T, row, needle string) {
	t.Helper()
	plain, dim := dimRunes(row)
	at := strings.Index(string(plain), needle)
	if at < 0 {
		t.Fatalf("no %q in the bar: %q", needle, string(plain))
	}
	start := len([]rune(string(plain)[:at]))
	for k := start; k < len(plain); k++ {
		if plain[k] != ' ' && !dim[k] {
			t.Fatalf("the bar turns bright at %q (rune %d) in %q\nraw %q", string(plain[k:]), k, string(plain), row)
		}
	}
}

// The bar is drawn faint. A mantra too long for the bar is cut with an
// ellipsis, and the cut used to append a full reset, so everything after the
// mantra (the context figure) was drawn bright until the mantra fit again:
// most often while an error alert was taking the room.
func TestTheBarStaysFaintAfterACutMantra(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	s := newSessionStatus("9c898301aaba3ad8d3c747f0b955a600", time.Unix(0, 0))
	s.metrics = aria.Metrics{Mantra: strings.Repeat("a mantra far too long for the bar ", 4), ContextTokens: 105}
	s.limit = 200_000
	s.setNoticeAt("provider: 529 overloaded, retrying in a while", alertError)
	rows := footerStanza(s, 100, "", "", false)
	assertDimFrom(t, rows[len(rows)-1], "105/")
}

// An interrupted turn paints its glyph in the state colour inside the faint
// bar; a full reset after it left the rest of the bar bright.
func TestTheBarStaysFaintAfterAnInterrupt(t *testing.T) {
	defer term.SetColorMode(term.ColorAlways)()
	s := newSessionStatus("9c898301aaba3ad8d3c747f0b955a600", time.Unix(0, 0))
	s.metrics = aria.Metrics{Mantra: "short", ContextTokens: 105}
	s.limit = 200_000
	s.setTurn(turnStatusInterrupted)
	rows := footerStanza(s, 100, "", "", true)
	assertDimFrom(t, rows[len(rows)-1], ariaName("9c898301aaba3ad8d3c747f0b955a600"))
}
