package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// Parallel tools whose combined output exceeds the viewport force the painter's
// viewport-overflow flush to commit the top tools' header rows to scrollback
// while they are still running. Scrollback is immutable, so the later completion
// glyph (✓) can never land there: pre-fix the live spinner frame froze in
// history and the user saw "⠙ bash ls /" stuck forever (observed in a real tmux
// capture, four parallel `ls` tools, first two frozen at ⠙). The painter must
// never commit an animated spinner frame to scrollback.
func TestVT_ParallelToolsOverflowNoFrozenSpinner(t *testing.T) {
	const W, H = 80, 12
	out := func(tag string) string {
		var b strings.Builder
		for i := 0; i < 8; i++ {
			b.WriteString(tag)
			b.WriteByte('-')
			b.WriteByte(byte('0' + i))
			if i < 7 {
				b.WriteByte('\n')
			}
		}
		return b.String()
	}
	run := func(cmd string) livedoc.Node { return bashTool(livedoc.StatusRunning, cmd, out(cmd)) }
	ok := func(cmd string) livedoc.Node { return bashTool(livedoc.StatusOK, cmd, out(cmd)) }

	// Four parallel tools come up running together (tall live region → overflow
	// flush fires while the top tools are still running), then all complete.
	states := [][]livedoc.Node{
		{run("ls /")},
		{run("ls /"), run("ls /usr")},
		{run("ls /"), run("ls /usr"), run("ls /var")},
		{run("ls /"), run("ls /usr"), run("ls /var"), run("ls /opt")},
		{ok("ls /"), ok("ls /usr"), ok("ls /var"), ok("ls /opt")},
	}

	got := driveH(W, H, func() string { return "BOOKEND" }, states)
	all := strings.Join(got, "\n")

	for _, f := range livedoc.SpinnerFrames {
		if strings.ContainsRune(all, f) {
			t.Fatalf("frozen spinner frame %q committed to scrollback (the stuck-spinner glitch):\n%s",
				string(f), all)
		}
	}
}
