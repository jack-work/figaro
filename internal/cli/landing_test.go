package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/term"
)

func landingText(w, h int, sendKey string) string {
	restore := term.SetColorMode(term.ColorNever)
	defer restore()
	return strings.Join(landingPage(w, h, "figaro v0.0.0", sendKey), "\n")
}

func TestTheLandingHelpNamesTheSendKeyAndTheNewRows(t *testing.T) {
	page := strings.Join(strings.Fields(landingText(160, 45, "ctrl+s")), " ")
	for _, want := range []string{
		"type > to compose a prompt (ctrl+s to send)",
		"type :new for a fresh figaro",
		"type :models to pick a model",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("landing page lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "fresh aria") {
		t.Errorf("landing page still says fresh aria:\n%s", page)
	}
	if strings.Index(page, ":models") < strings.Index(page, ":state") {
		t.Errorf(":models sits above :state:\n%s", page)
	}
}

func TestTheLandingSendHintFollowsTheLiveKeymap(t *testing.T) {
	if got := landingSendKey(); got != "ctrl+s" {
		t.Fatalf("default send key = %q, want ctrl+s", got)
	}

	withUserKeys(t, binds("command", "^S", "none", "^O", "cmd-search-next"))
	if got := landingSendKey(); got != "ctrl+o" {
		t.Fatalf("rebound send key = %q, want ctrl+o", got)
	}

	withUserKeys(t, binds("command", "^S", "none"))
	if got := landingSendKey(); got != "" {
		t.Fatalf("unbound send key = %q, want none", got)
	}
	page := landingText(160, 45, landingSendKey())
	if !strings.Contains(page, "to compose a prompt\n") || strings.Contains(page, "to send)") {
		t.Fatalf("an unbound send key still shows a parenthetical:\n%s", page)
	}
}

func TestOnlyTheLandingArtIsPaintedGreen(t *testing.T) {
	t.Setenv("COLORTERM", "truecolor")
	restore := term.SetColorMode(term.ColorAlways)
	defer restore()
	green := term.Brand("x")
	green = green[:strings.Index(green, "x")]

	page := landingPage(160, 45, "figaro v0.0.0", "ctrl+s")
	var art, other int
	for _, row := range page {
		switch {
		case strings.Contains(row, "$$"):
			art++
			if !strings.Contains(row, green) {
				t.Errorf("an art row is not green: %q", row)
			}
		case row != "":
			other++
			if strings.Contains(row, green) {
				t.Errorf("a non-art row is green: %q", row)
			}
		}
	}
	if art == 0 || other == 0 {
		t.Fatalf("art rows %d, other rows %d: the page did not draw both", art, other)
	}
}

func TestTheLandingPageDropsArtThenHelpThenEverything(t *testing.T) {
	cases := []struct {
		w, h             int
		art, help, title bool
		hint             bool
	}{
		{160, 45, true, true, true, true},
		{100, 30, true, true, true, true},
		{60, 20, false, true, true, true},
		{60, 30, true, true, true, true},
		{58, 20, false, true, true, false},
		{70, 12, false, true, true, true},
		{70, 11, false, false, true, false},
		{40, 12, false, false, true, false},
		{20, 1, false, false, false, false},
	}
	for _, tc := range cases {
		page := landingText(tc.w, tc.h, "ctrl+s")
		rows := strings.Split(page, "\n")
		if len(rows) > tc.h {
			t.Errorf("%dx%d: %d rows", tc.w, tc.h, len(rows))
		}
		for _, r := range rows {
			if n := len([]rune(r)); n > tc.w {
				t.Errorf("%dx%d: a row is %d wide: %q", tc.w, tc.h, n, r)
			}
		}
		got := [4]bool{
			strings.Contains(page, "$$"),
			strings.Contains(page, "type  q"),
			strings.Contains(page, "by John Kelliher"),
			strings.Contains(page, "ctrl+s to send"),
		}
		want := [4]bool{tc.art, tc.help, tc.title, tc.hint}
		if got != want {
			t.Errorf("%dx%d: art/help/title/hint = %v, want %v:\n%s", tc.w, tc.h, got, want, page)
		}
	}
}
func TestLoginHeadsTheLandingHelp(t *testing.T) {
	var keys []string
	for _, r := range landingHelpRows("ctrl+s") {
		keys = append(keys, r[0])
	}
	if got, want := strings.Join(keys, " "), ":login > :new :fork -- <text> :state :models :ls :help q"; got != want {
		t.Errorf("%s\nwant %s", got, want)
	}
}
