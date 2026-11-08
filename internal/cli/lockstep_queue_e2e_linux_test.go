//go:build linux

package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gen5 t34 (bugs.md #5): a prompt queued while the aria was idle drew nine
// steps after the frame that queued it, held back by the live debut grace,
// which hides a row the daemon lifts within a few frames. A lockstep step
// draws what the tape has by then; a row lifted within the step never draws.
func TestALockstepQueueRowDrawsOnTheStepItArrives(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the binary in a pty; skipped under -short")
	}
	src, err := os.Open(filepath.Join("testdata", "tapes", "stream-eager.tape"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(t.TempDir(), "debut.tape")
	dst, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	sc.Scan()
	dst.WriteString(sc.Text() + "\n")
	for _, line := range []string{
		`{"t":0.0001,"dir":"out","msg":{"jsonrpc":"2.0","id":902,"method":"figaro.form","params":{"figaro_id":"e4219ad4","intrinsic":"queue"}}}`,
		`{"t":0.0001,"dir":"out","msg":{"jsonrpc":"2.0","id":905,"method":"figaro.form","params":{"figaro_id":"e4219ad4","intrinsic":"runtime"}}}`,
		`{"t":0.0002,"dir":"in","msg":{"jsonrpc":"2.0","id":902,"result":{"snapshot":{"epoch":"q1","len":0,"order":null},"version":1,"intrinsic":"queue","incarnation":7,"access":{"mode":"read"}}}}`,
		`{"t":0.0002,"dir":"in","msg":{"jsonrpc":"2.0","id":905,"result":{"snapshot":{"inflight":0,"turn":{"id":1,"state":"idle"}},"version":1,"intrinsic":"runtime","incarnation":7,"access":{"mode":"read"}}}}`,
		`{"t":0.5,"dir":"in","msg":{"jsonrpc":"2.0","method":"form.delta","params":{"figaro_id":"e4219ad4","schema":2,"aria_id":"e4219ad4","intrinsic":"queue","incarnation":7,"version":2,"patch":{"object":{"Update":{"items":{"object":{"Update":{"1":{"object":{"Set":{"at":1,"state":"committing","text":"queued while idle"}}}},"New":{"1":true}}},"len":{"scalar":{"Before":0,"After":1}}},"New":{"items":true}}}}}}`,
	} {
		dst.WriteString(line + "\n")
	}
	for sc.Scan() {
		dst.WriteString(sc.Text() + "\n")
	}
	dst.Close()

	run := lockstepDrive(t, lockstepBinary(t), path, 60, nil, 0)
	for n, b := range run.steps {
		if strings.Contains(string(b), "queued while idle") {
			if n != 30 {
				t.Fatalf("the prompt queued at step 30 drew at step %d", n)
			}
			return
		}
	}
	t.Fatal("the queued prompt never drew")
}
