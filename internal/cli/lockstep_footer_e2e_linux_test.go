//go:build linux

package cli

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/livedoc"
)

// gen5 t29 under the stage manager's driver: the footer read blank, then ✓,
// through a turn the tape's runtime form says was thinking. The driver ran
// inside an aria, so every request carried FIGARO_ARIA's caller id, missed
// the recorded answers and got the method's latest answer instead: the
// runtime seed came back as the host form. A lockstep footer shows the
// state the tape's runtime form records, whoever drives it.
func TestALockstepFooterShowsTheTapesTurnState(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the binary in a pty; skipped under -short")
	}
	src, err := os.Open(filepath.Join("testdata", "tapes", "stream-eager.tape"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(t.TempDir(), "runtime.tape")
	dst, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	sc.Scan()
	dst.WriteString(sc.Text() + "\n")
	for _, line := range []string{
		`{"t":0.0001,"dir":"out","msg":{"jsonrpc":"2.0","id":903,"method":"figaro.form","params":{"figaro_id":"e4219ad4","x-internal-figaro-id":"e83ae209","x-caller":{"duke":true}}}}`,
		`{"t":0.0001,"dir":"out","msg":{"jsonrpc":"2.0","id":905,"method":"figaro.form","params":{"figaro_id":"e4219ad4","intrinsic":"runtime","x-internal-figaro-id":"e83ae209","x-caller":{"duke":true}}}}`,
		`{"t":0.0002,"dir":"in","msg":{"jsonrpc":"2.0","id":905,"result":{"snapshot":{"inflight":0,"turn":{"id":1,"state":"idle"}},"version":9,"intrinsic":"runtime","incarnation":7,"access":{"mode":"read"}}}}`,
		`{"t":0.0003,"dir":"in","msg":{"jsonrpc":"2.0","id":903,"result":{"snapshot":{"aria_id":"e4219ad4"},"version":4,"incarnation":7,"access":{"mode":"read"}}}}`,
		`{"t":1.0,"dir":"in","msg":{"jsonrpc":"2.0","method":"form.delta","params":{"figaro_id":"e4219ad4","schema":2,"aria_id":"e4219ad4","intrinsic":"runtime","incarnation":7,"version":10,"patch":{"object":{"Update":{"turn":{"object":{"Update":{"state":{"scalar":{"Before":"idle","After":"thinking"}}}}}}}}}}}`,
		`{"t":3.0,"dir":"in","msg":{"jsonrpc":"2.0","method":"form.delta","params":{"figaro_id":"e4219ad4","schema":2,"aria_id":"e4219ad4","intrinsic":"runtime","incarnation":7,"version":11,"patch":{"object":{"Update":{"turn":{"object":{"Set":{"reason":"stop"},"Update":{"state":{"scalar":{"Before":"thinking","After":"idle"}}}}}}}}}}}`,
	} {
		dst.WriteString(line + "\n")
	}
	for sc.Scan() {
		dst.WriteString(sc.Text() + "\n")
	}
	dst.Close()

	run := lockstepDrive(t, lockstepBinary(t), path, 250, []string{"FIGARO_ARIA=0123456789abcdef0123456789abcdef"}, 0)
	spinning := string(livedoc.SpinnerFrames)
	for _, want := range []struct {
		step int
		ok   func(string) bool
		say  string
	}{
		{120, func(g string) bool { return g != "" && strings.Contains(spinning, g) }, "the spinner of a thinking turn"},
		{170, func(g string) bool { return g != "" && strings.Contains(spinning, g) }, "the spinner of a thinking turn"},
		{240, func(g string) bool { return g == "✓" }, "✓ for a turn that stopped"},
	} {
		if g := footerGlyph(run.steps[:want.step+1], 34); !want.ok(g) {
			t.Errorf("step %d: the footer shows %q, want %s", want.step, g, want.say)
		}
	}
}

var sgr = regexp.MustCompile(`^\x1b\[[0-9;:]*m`)

func footerGlyph(steps [][]byte, row int) string {
	all := bytes.Join(steps, nil)
	at := []byte("\x1b[" + strconv.Itoa(row) + ";1H")
	i := bytes.LastIndex(all, at)
	if i < 0 {
		return ""
	}
	rest := all[i+len(at):]
	for {
		m := sgr.Find(rest)
		if m == nil {
			break
		}
		rest = rest[len(m):]
	}
	r, _ := utf8.DecodeRune(rest)
	if r == utf8.RuneError || r == '\x1b' || r == ' ' {
		return ""
	}
	return string(r)
}
