package store

// Does an acknowledged patch survive a kill?
//
// That is the whole claim sync-before-publish makes, and until this test it
// was a comment. A child process patches a form and prints every version it
// was told landed; the parent kills it at a random moment, reopens the store,
// and checks that every acknowledged version is there.
//
// The shape is figwal's crashtest harness, narrowed to one form: a child
// re-entering the test binary through an env var, killed with SIGKILL so no
// deferred close can tidy anything away.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/message"
)

const crashChildEnv = "FIGARO_FORM_CRASH_CHILD"

// crashChild patches forever, printing "ok <version>" for every patch the
// writer said landed. It is killed mid-flight and never returns.
func crashChild(dir string) {
	be, err := NewXwalBackend(dir, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child open:", err)
		os.Exit(2)
	}
	id, _, err := be.CreateForm("", patchSet(map[string]string{"seed": "0"}))
	if err != nil {
		fmt.Fprintln(os.Stderr, "child create:", err)
		os.Exit(2)
	}
	fmt.Printf("form %s\n", id)
	out := bufio.NewWriter(os.Stdout)
	for i := 0; ; i++ {
		raw, _ := json.Marshal(fmt.Sprintf("v%d", i))
		v, err := be.ApplyForm(id, message.Patch{Set: map[string]json.RawMessage{
			fmt.Sprintf("k%d", i): raw,
		}})
		if err != nil {
			continue
		}
		// Printed and FLUSHED only after the writer acknowledged, so the
		// parent's list is exactly the set of patches that were promised.
		fmt.Fprintf(out, "ok %d %d\n", i, v)
		out.Flush()
	}
}

func TestAcknowledgedPatchesSurviveAKill(t *testing.T) {
	// Opt-in: it spawns and kills child processes, and it is meaningless on
	// tmpfs, where a "sync" costs nothing and the child spins fast enough to
	// bury the parent in acknowledgements.
	//
	//	FIGARO_CRASH_TEST=1 TMPDIR=/var/tmp go test ./internal/store -run Acknowledged -v
	if os.Getenv("FIGARO_CRASH_TEST") == "" {
		t.Skip("set FIGARO_CRASH_TEST=1 (and TMPDIR to real disk) to run the kill test")
	}
	rng := rand.New(rand.NewSource(1))
	for attempt := 0; attempt < 4; attempt++ {
		dir := t.TempDir()
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), crashChildEnv+"="+dir)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		acked := map[string]string{}
		formID := ""
		done := make(chan struct{})
		go func() {
			defer close(done)
			sc := bufio.NewScanner(stdout)
			for sc.Scan() {
				f := strings.Fields(sc.Text())
				if len(f) == 2 && f[0] == "form" {
					formID = f[1]
					continue
				}
				if len(f) == 3 && f[0] == "ok" {
					acked["k"+f[1]] = "v" + f[1]
				}
			}
		}()

		time.Sleep(time.Duration(300+rng.Intn(700)) * time.Millisecond)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		<-done

		if formID == "" || len(acked) == 0 {
			t.Logf("attempt %d: killed before anything was acknowledged; nothing to check", attempt)
			continue
		}

		be, err := NewXwalBackend(dir, 0)
		if err != nil {
			t.Fatalf("attempt %d: reopen: %v", attempt, err)
		}
		snap, err := be.FormState(formID)
		if err != nil {
			be.Close()
			t.Fatalf("attempt %d: fold %s: %v", attempt, formID, err)
		}
		for k, want := range acked {
			got, ok := snap.Get(k)
			if !ok {
				be.Close()
				t.Fatalf("attempt %d: %s was acknowledged and is not on disk (%d acked)",
					attempt, k, len(acked))
			}
			if s, _ := strconv.Unquote(string(got)); s != want {
				be.Close()
				t.Fatalf("attempt %d: %s holds %s, was acknowledged as %s", attempt, k, got, want)
			}
		}
		be.Close()
		t.Logf("attempt %d: %d acknowledged patches, all durable", attempt, len(acked))
	}
}
