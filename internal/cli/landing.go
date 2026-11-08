package cli

import (
	"embed"
	"strings"
	"sync"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/term"
)

//go:embed landing/figaro-*.txt
var landingFS embed.FS

var landingArt = loadLandingArt("landing/figaro-78.txt", "landing/figaro-60.txt", "landing/figaro-44.txt")

func loadLandingArt(names ...string) [][]string {
	arts := make([][]string, 0, len(names))
	for _, name := range names {
		raw, err := landingFS.ReadFile(name)
		if err != nil {
			continue
		}
		arts = append(arts, strings.Split(strings.TrimRight(string(raw), "\n"), "\n"))
	}
	return arts
}

func landingHelpRows(sendKey string) [][2]string {
	compose := "to compose a prompt"
	if sendKey != "" {
		compose += " (" + sendKey + " to send)"
	}
	return [][2]string{
		{":login", "to sign in to a provider"},
		{">", compose},
		{":new", "for a fresh figaro"},
		{":fork -- <text>", "to branch, and prompt the branch"},
		{":state", "for config and agent-visible state"},
		{":models", "to pick a model"},
		{":ls", "for the figaro tree"},
		{":help", "for help"},
		{"q", "to quit"},
	}
}

func landingSendKey() string {
	for i := range liveRows {
		bd := &liveRows[i]
		if bd.modes&inBox != 0 && bd.action == "cmd-search-next" {
			return friendlyKey(bd.chord.spell())
		}
	}
	return ""
}

func friendlyKey(spelled string) string {
	if rest, ok := strings.CutPrefix(spelled, "^"); ok && rest != "" {
		return "ctrl+" + strings.ToLower(rest)
	}
	if rest, ok := strings.CutPrefix(spelled, "M-"); ok && rest != "" {
		return "alt+" + strings.ToLower(rest)
	}
	return strings.ToLower(spelled)
}

const landingKeyColumn = 18

func (t *transcript) landing() bool {
	return t.index.total == 0 && t.client.Heard() && t.client.Count() == 0 && t.client.Open() == nil
}

var landingVersion = sync.OnceValue(func() string {
	if r := boot.BuildRelease(); r != "" {
		return "figaro v" + strings.TrimPrefix(r, "v")
	}
	if rev := boot.BuildRevision(); rev != "" {
		return "figaro (dev " + rev[:min(len(rev), 12)] + ")"
	}
	return "figaro (dev)"
})

type landingStanza struct {
	rows   []string
	style  func(string) string
	center bool
}

func (s landingStanza) width() int {
	n := 0
	for _, r := range s.rows {
		n = max(n, runewidth.StringWidth(r))
	}
	return n
}

func landingPage(w, h int, version, sendKey string) []string {
	info := []landingStanza{
		{rows: []string{version}, style: term.Label, center: true},
		{rows: []string{"by John Kelliher, et al."}, style: plainStyle, center: true},
	}
	need := len(info)
	if need > h || info[0].width() > w || info[1].width() > w {
		return nil
	}
	page := info
	if help, ok := landingHelpFor(w, h-need-1, sendKey); ok {
		page = append(page, landingStanza{rows: []string{""}, style: plainStyle}, help)
		need += 1 + len(help.rows)
	}
	if art := landingArtFor(w, h-need-1); art != nil {
		page = append([]landingStanza{{rows: art, style: term.Brand}, {rows: []string{""}, style: plainStyle}}, page...)
		need += len(art) + 1
	}

	out := make([]string, 0, h)
	for range (h - need) / 2 {
		out = append(out, "")
	}
	for _, s := range page {
		bw := s.width()
		for _, r := range s.rows {
			if s.center {
				bw = runewidth.StringWidth(r)
			}
			lead := strings.Repeat(" ", max((w-bw)/2, 0))
			if r == "" {
				out = append(out, "")
				continue
			}
			out = append(out, lead+s.style(r))
		}
	}
	return out
}

func landingArtFor(w, rows int) []string {
	for _, art := range landingArt {
		s := landingStanza{rows: art}
		if s.width() <= w-2 && len(art) <= rows {
			return art
		}
	}
	return nil
}

func landingHelpFor(w, rows int, sendKey string) (landingStanza, bool) {
	for _, key := range []string{sendKey, ""} {
		help := landingStanza{rows: landingHelpText(landingHelpRows(key)), style: styleLandingHelp}
		if len(help.rows) <= rows && help.width() <= w {
			return help, true
		}
		if key == "" {
			break
		}
	}
	return landingStanza{}, false
}

func landingHelpText(rows [][2]string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = "type  " + r[0] + strings.Repeat(" ", max(landingKeyColumn-runewidth.StringWidth(r[0]), 2)) + r[1]
	}
	return out
}

func plainStyle(s string) string { return s }

func styleLandingHelp(line string) string {
	rest, ok := strings.CutPrefix(line, "type  ")
	if !ok {
		return line
	}
	keys, does, _ := strings.Cut(rest, "  ")
	return "type  " + term.Cyan(keys) + "  " + does
}
