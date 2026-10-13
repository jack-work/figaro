package crashtest

import (
	"bufio"
	"encoding/json"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	truncateOld = 40
	truncateCut = 17
	truncateNew = 30
)

type truncatePayload struct {
	G string `json:"g"`
	Q uint64 `json:"q"`
}

func truncateRow(g string, q uint64) []byte {
	b, _ := json.Marshal(truncatePayload{G: g, Q: q})
	return b
}

// runTruncateChild cuts the notes channel and re-appends behind the cut; the
// parent kills it anywhere in between.
func runTruncateChild() {
	dir, trunk := os.Getenv("CRASH_DIR"), os.Getenv("CRASH_TRUNK")
	st, err := openStore(dir)
	if err != nil {
		childFail(err)
	}
	ts := st.(*trunksStore)
	if err := ts.s.TruncateFrom(trunk, chanNotes, truncateCut); err != nil {
		childFail(err)
	}
	emit("cut")
	for q := uint64(1); q <= truncateNew; q++ {
		lt, err := st.AppendChannel(trunk, chanNotes, truncateOld, truncateRow("new", q))
		if err != nil {
			childFail(err)
		}
		if err := ts.s.Trunks.SyncChannelThrough(trunk, chanNotes, lt); err != nil {
			childFail(err)
		}
		emit("a %d", q)
		time.Sleep(time.Millisecond)
	}
	if err := st.Close(); err != nil {
		childFail(err)
	}
	emit("closed")
	os.Exit(0)
}

// A kill anywhere in TruncateFrom-then-append leaves the channel readable as
// the old rows, or the rows below the cut followed by a prefix of the new
// ones; never a torn or misplaced row.
func TestTruncateFromSurvivesAKill(t *testing.T) {
	dir := t.TempDir()
	st, trunk, err := createStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= truncateOld; i++ {
		if _, err := st.AppendMain(trunk, truncateRow("main", i)); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AppendChannel(trunk, chanNotes, i, truncateRow("old", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	_, seed := testSeed(t)
	rng := rand.New(rand.NewSource(seed))
	cycles := 14
	if *longFlag {
		cycles = 80
	}
	var sawOld, sawCut, sawPartial, sawClosed int
	for cycle := 0; cycle < cycles; cycle++ {
		delay := time.Duration(rng.Intn(60)) * time.Millisecond
		lines := runTruncateChildOnce(t, dir, trunk, delay)
		cut := len(lines) > 0 && lines[0] == "cut"
		rows := readNotes(t, dir, trunk)
		old := 0
		for old < len(rows) && rows[old].G == "old" {
			old++
		}
		switch {
		case old == truncateOld && len(rows) == truncateOld:
			if cut {
				t.Fatalf("cycle %d: the child reported the cut, the store shows the old tail", cycle)
			}
			sawOld++
		case old == truncateCut-1:
			sawCut++
			if n := len(rows) - old; n > 0 && n < truncateNew {
				sawPartial++
			}
			if lines[len(lines)-1] == "closed" {
				sawClosed++
				if len(rows)-old != truncateNew {
					t.Fatalf("cycle %d: a clean close kept %d new rows, want %d", cycle, len(rows)-old, truncateNew)
				}
			}
			for j, r := range rows[old:] {
				if r.G != "new" || r.Q != uint64(j+1) {
					t.Fatalf("cycle %d: row %d after the cut is %+v, want new #%d", cycle, old+j, r, j+1)
				}
			}
		default:
			t.Fatalf("cycle %d (kill after %v, child lines %d): %d old rows of %d total, want %d or %d",
				cycle, delay, len(lines), old, len(rows), truncateOld, truncateCut-1)
		}
		for i := 1; i < old; i++ {
			if rows[i-1].Q != uint64(i) {
				t.Fatalf("cycle %d: old row %d carries q=%d", cycle, i, rows[i-1].Q)
			}
		}
		// Reseed so every cycle cuts something.
		reseedNotes(t, dir, trunk)
	}
	t.Logf("outcomes: old tail %d, cut %d (partial re-append %d, clean close %d)", sawOld, sawCut, sawPartial, sawClosed)
	if sawCut == 0 {
		t.Fatal("no cycle observed the cut; widen the kill window")
	}
}

func runTruncateChildOnce(t *testing.T, dir, trunk string, delay time.Duration) []string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "FIGWAL_TRUNCATE_CHILD=1", "CRASH_DIR="+dir, "CRASH_TRUNK="+trunk)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan []string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		var lines []string
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		done <- lines
	}()
	time.Sleep(delay)
	_ = cmd.Process.Kill()
	lines := <-done
	_ = cmd.Wait()
	if cmd.ProcessState.ExitCode() != -1 && cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("child failed: %s", stderr.String())
	}
	return lines
}

func readNotes(t *testing.T, dir, trunk string) []truncatePayload {
	t.Helper()
	st, err := openStore(dir)
	if err != nil {
		t.Fatalf("open after kill: %v", err)
	}
	defer st.Close()
	recs, err := st.ReadAll(trunk, chanNotes)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]truncatePayload, 0, len(recs))
	for i, r := range recs {
		if r.ChanLT != uint64(i+1) {
			t.Fatalf("record %d sits at channel LT %d", i, r.ChanLT)
		}
		var p truncatePayload
		if err := json.Unmarshal(r.Payload, &p); err != nil || (p.G != "old" && p.G != "new") {
			t.Fatalf("record %d is torn: %q (%v)", r.ChanLT, r.Payload, err)
		}
		out = append(out, p)
	}
	return out
}

// reseedNotes restores the old tail: whatever landed after the cut is cut
// again and the old rows re-appended.
func reseedNotes(t *testing.T, dir, trunk string) {
	t.Helper()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ts := st.(*trunksStore)
	if err := ts.s.TruncateFrom(trunk, chanNotes, truncateCut); err != nil {
		t.Fatal(err)
	}
	for i := uint64(truncateCut); i <= truncateOld; i++ {
		if _, err := st.AppendChannel(trunk, chanNotes, i, truncateRow("old", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readNotes(t, dir, trunk); len(got) != truncateOld {
		t.Fatalf("reseed left %d rows", len(got))
	}
}
