//go:build linux

package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
)

// gen5 t34, a fork's pane: its recorder attached while the fork was
// dormant, so the first reads of the queue and runtime forms answered
// "is dormant" and the first deltas arrived in the same batch as the
// recorded resync. The resync answered the older recorded snapshot, the
// delta after it no longer followed on, and every later delta resynced to
// that same snapshot: the footer spun through every idle stretch to the
// end of the tape (bugs.md #4). A form read in a replay answers the
// tape's state as of now.
func TestALockstepForkAttachedDormantSettlesOnItsTape(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the binary in a pty; skipped under -short")
	}
	src, err := os.Open(filepath.Join("testdata", "tapes", "stream-eager.tape"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(t.TempDir(), "dormant.tape")
	dst, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	sc.Scan()
	dst.WriteString(sc.Text() + "\n")
	const (
		dormant = `{"code":-32000,"message":"e4219ad4 is dormant, so it has no \"%s\": an intrinsic form is live-only state"}`
		runtime = `"figaro_id":"e4219ad4","schema":2,"aria_id":"e4219ad4","intrinsic":"runtime","incarnation":7`
		queue   = `"figaro_id":"e4219ad4","schema":2,"aria_id":"e4219ad4","intrinsic":"queue","incarnation":7`
	)
	for _, line := range []string{
		`{"t":0.0001,"dir":"out","msg":{"jsonrpc":"2.0","id":902,"method":"figaro.form","params":{"figaro_id":"e4219ad4","intrinsic":"queue"}}}`,
		`{"t":0.0001,"dir":"out","msg":{"jsonrpc":"2.0","id":905,"method":"figaro.form","params":{"figaro_id":"e4219ad4","intrinsic":"runtime"}}}`,
		`{"t":0.0002,"dir":"in","msg":{"jsonrpc":"2.0","id":902,"error":` + strings.Replace(dormant, "%s", "queue", 1) + `}}`,
		`{"t":0.0002,"dir":"in","msg":{"jsonrpc":"2.0","id":905,"error":` + strings.Replace(dormant, "%s", "runtime", 1) + `}}`,
		`{"t":0.5,"dir":"in","msg":{"jsonrpc":"2.0","method":"form.delta","params":{` + queue + `,"version":2,"patch":{"object":{"Update":{"items":{"object":{"Update":{"1":{"object":{"Set":{"at":1,"state":"committing","text":"queued at the attach"}}}},"New":{"1":true}}},"len":{"scalar":{"Before":0,"After":1}}},"New":{"items":true}}}}}}`,
		`{"t":0.5,"dir":"in","msg":{"jsonrpc":"2.0","method":"form.delta","params":{` + runtime + `,"version":2,"patch":{"object":{"Update":{"inflight":{"scalar":{"Before":0,"After":1}},"turn":{"object":{"Update":{"state":{"scalar":{"Before":"idle","After":"accepted"}}}}}}}}}}}`,
		`{"t":0.5,"dir":"in","msg":{"jsonrpc":"2.0","method":"form.delta","params":{` + runtime + `,"version":3,"patch":{"object":{"Update":{"turn":{"object":{"Update":{"state":{"scalar":{"Before":"accepted","After":"committing"}}}}}}}}}}}`,
		`{"t":0.5,"dir":"out","msg":{"jsonrpc":"2.0","id":907,"method":"figaro.form","params":{"figaro_id":"e4219ad4","intrinsic":"runtime"}}}`,
		`{"t":0.5,"dir":"in","msg":{"jsonrpc":"2.0","id":907,"result":{"snapshot":{"inflight":1,"turn":{"id":0,"state":"accepted"}},"version":2,"intrinsic":"runtime","incarnation":7,"access":{"mode":"read"}}}}`,
		`{"t":1.0,"dir":"in","msg":{"jsonrpc":"2.0","method":"form.delta","params":{` + runtime + `,"version":4,"patch":{"object":{"Update":{"turn":{"object":{"Update":{"state":{"scalar":{"Before":"committing","After":"thinking"}}}}}}}}}}}`,
		`{"t":2.0,"dir":"in","msg":{"jsonrpc":"2.0","method":"form.delta","params":{` + runtime + `,"version":5,"patch":{"object":{"Update":{"inflight":{"scalar":{"Before":1,"After":0}},"turn":{"object":{"Set":{"reason":"stop"},"Update":{"state":{"scalar":{"Before":"thinking","After":"idle"}}}}}}}}}}}`,
	} {
		dst.WriteString(line + "\n")
	}
	for sc.Scan() {
		dst.WriteString(sc.Text() + "\n")
	}
	dst.Close()

	run := lockstepDrive(t, lockstepBinary(t), path, 290, nil, 0)
	if g := footerGlyph(run.steps[:91], 34); g == "" || !strings.Contains(string(livedoc.SpinnerFrames), g) {
		t.Errorf("step 90: the footer shows %q, want the spinner of the thinking turn", g)
	}
	if g := footerGlyph(run.steps[:151], 34); g != "✓" {
		t.Errorf("step 150: the footer shows %q, want ✓ for the turn that stopped at step 120", g)
	}
	for n := 125; n < 290; n++ {
		if len(run.steps[n]) > 0 {
			t.Errorf("step %d drew %q while the fork was idle: nothing is due before the tape's next frame at step 300", n, clip(run.steps[n]))
			break
		}
	}
}
