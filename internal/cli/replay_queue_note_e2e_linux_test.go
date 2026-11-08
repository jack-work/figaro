//go:build linux

package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// gen4 take 7: a replay opened with "message queue is empty" on the bar
// though nobody opened the queue. The recorded prompt passes through the
// queue intrinsic (committing, then committed), the pit opens for it by
// itself and closes when it drains, and the close said so. A replay has no
// keystrokes of the tape's user, so a pit it opened by itself closes
// without a word.
func TestAReplaySaysNothingWhenAQueueItOpenedByItselfDrains(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the binary in a pty; skipped under -short")
	}
	r := newRestartRig(t)
	if err := os.WriteFile(filepath.Join(r.root, "cache", "ambiwidth-xterm-256color"), []byte("narrow"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(filepath.Join("testdata", "tapes", "stream-eager.tape"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(t.TempDir(), "queue.tape")
	dst, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	sc.Scan()
	dst.WriteString(sc.Text() + "\n")
	for _, line := range []string{
		`{"t":0.0001,"dir":"out","msg":{"jsonrpc":"2.0","id":901,"method":"figaro.form","params":{"figaro_id":"e4219ad4","intrinsic":"queue"}}}`,
		`{"t":0.0002,"dir":"in","msg":{"jsonrpc":"2.0","id":901,"result":{"snapshot":{"epoch":"q1","len":0,"order":null},"version":1,"intrinsic":"queue","incarnation":7,"access":{"mode":"read"}}}}`,
		`{"t":0.5,"dir":"in","msg":{"jsonrpc":"2.0","method":"form.delta","params":{"figaro_id":"e4219ad4","schema":2,"aria_id":"e4219ad4","intrinsic":"queue","incarnation":7,"version":2,"patch":{"object":{"Update":{"items":{"object":{"Update":{"1":{"object":{"Set":{"at":1,"state":"committing","text":"queued by the tape"}}}},"New":{"1":true}}},"len":{"scalar":{"Before":0,"After":1}}},"New":{"items":true}}}}}}`,
		`{"t":1.5,"dir":"in","msg":{"jsonrpc":"2.0","method":"form.delta","params":{"figaro_id":"e4219ad4","schema":2,"aria_id":"e4219ad4","intrinsic":"queue","incarnation":7,"version":3,"patch":{"object":{"Update":{"items":{"object":{"Update":{"1":{"object":{"Update":{"state":{"scalar":{"Before":"committing","After":"committed"}}}}}}}}}}}}}}`,
	} {
		dst.WriteString(line + "\n")
	}
	for sc.Scan() {
		dst.WriteString(sc.Text() + "\n")
	}
	dst.Close()

	out := replayInPtyAt(t, r, path, "1")
	if !screenShows(out, "queued by the tape", 20*time.Second) {
		t.Fatalf("the queue pit never showed the tape's queued prompt; screen tail:\n%q", out.tail(800))
	}
	if screenShows(out, queueEmptyNote, 3*time.Second) {
		t.Fatalf("the replay said %q when a queue it opened by itself drained", queueEmptyNote)
	}
}
