//go:build linux

package cli

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// An empty aria opens on the landing page, sized to the pane, and the page
// gives way to the conversation at the first message.
func TestAnEmptyAriaOpensOnTheLandingPage(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns daemons; skipped under -short")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	const art = "$$$$$$$$$$$$$$$$"
	cases := []struct {
		name    string
		w, h    int
		art     bool
		helpRow bool
	}{
		{"a wide pane draws the art", 110, 40, true, true},
		{"a short pane drops the art and keeps the help", 70, 16, false, true},
		{"a narrow pane keeps only the version", 40, 12, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRestartRig(t)
			r.env = append(r.env, "ANTHROPIC_BASE_URL="+replyProvider(t), "FIGARO_NO_BIND=1")
			id := r.newEmptyAria()
			p := newPane(t, r.env, r.bin, tc.w, tc.h)
			p.send(r.bin + " listen " + id)
			p.key("Enter")
			waitPane(t, p, "by John Kelliher, et al.", "the landing page never showed on an empty aria")
			screen := p.visible()
			if got := strings.Contains(screen, art); got != tc.art {
				t.Fatalf("art drawn: %v, want %v:\n%s", got, tc.art, screen)
			}
			if got := strings.Contains(screen, ":new"); got != tc.helpRow {
				t.Fatalf("help rows drawn: %v, want %v:\n%s", got, tc.helpRow, screen)
			}
			for _, line := range strings.Split(screen, "\n") {
				if w := len([]rune(line)); w > tc.w {
					t.Fatalf("a landing row is %d wide in a %d-wide pane: %q", w, tc.w, line)
				}
			}

			r.run("send", "-f", "--id", id, "--", "hello")
			waitPane(t, p, "answer-number-1", "the first reply never showed")
			if screen := p.visible(); strings.Contains(screen, "by John Kelliher") {
				t.Fatalf("the landing page outlived the first message:\n%s", screen)
			}
		})
	}
}

func waitPane(t *testing.T, p *pane, text, why string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(p.visible(), text) {
		if time.Now().After(deadline) {
			t.Fatalf("%s:\n%s", why, p.visible())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (r *restartRig) newEmptyAria() string {
	r.t.Helper()
	out := r.run("new", "-j")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var born struct {
		AriaID string `json:"aria_id"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &born); err != nil || born.AriaID == "" {
		r.t.Fatalf("new -j printed no aria id: %q", out)
	}
	return born.AriaID
}
