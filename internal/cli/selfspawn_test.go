package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

// The fork bomb, and its seatbelt.
//
// ensureAngelus starts the daemon by re-executing ITSELF — os.Executable()
// + exec.Command(exe) + _FIGARO_DAEMON=1, detached (angelus_client.go). In a
// test binary os.Executable() is `cli.test`, so any test that reaches a
// daemon-connecting path spawns a detached copy of the TEST BINARY, which
// re-runs the suite, which reaches the path again, which spawns another.
//
// 2026-07-30 01:43: a canary run of TestJSONArgvIsRejectedNotIgnored — which
// called runSendAs(nil, …) directly, and with the guard under test neutered
// fell through to runSendRaw -> mustConnectAngelus — put 1391 concurrent
// cli.test processes in the kernel's OOM task dump. Two bursts (376, then
// 1013: the doubling), span 6423, 45.8G RAM + 50.2G swap, global OOM at
// 01:43:14, graphical session torn down at 01:43:54, compositor SIGTERM'd.
//
// Two lessons, both encoded here:
//   - The seatbelt: refuseSelfSpawn dies rather than forking a test binary.
//   - Not driving into the wall: the tests that provoked it assert on pure
//     predicates now (see json_contract_test.go), never on a dispatcher that
//     can reach a socket.

func TestIsTestBinary(t *testing.T) {
	cases := []struct {
		exe  string
		want bool
	}{
		{"/tmp/go-build123/b001/cli.test", true},
		{"cli.test", true},
		{`C:\tmp\cli.test.exe`, true},
		{"/nix/store/abc-figaro-0.15.4/bin/figaro", false},
		{"/home/gluck/.nix-profile/bin/fig", false},
		{"/var/tmp/x/figaro", false},
		// Not a suffix match on the whole path: a figaro living under a
		// directory called something.test is still figaro.
		{"/home/gluck/some.test/figaro", false},
	}
	for _, tc := range cases {
		if got := isTestBinary(tc.exe); got != tc.want {
			t.Errorf("isTestBinary(%q) = %v, want %v", tc.exe, got, tc.want)
		}
	}
}

// TestRefuseSelfSpawnFromTestBinary is the canary for the guard itself: the
// running binary IS a test binary, so the real os.Executable() must trip it.
func TestRefuseSelfSpawnFromTestBinary(t *testing.T) {
	// Isolate the test-binary branch from the env branch: the standing test
	// recipe exports FIGARO_NO_SELF_SPAWN=1, which would refuse first and
	// for a different reason.
	t.Setenv("FIGARO_NO_SELF_SPAWN", "")
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("no executable path: %s", err)
	}
	if !isTestBinary(exe) {
		t.Fatalf("this test must run from a test binary, got %q", exe)
	}

	var code int
	var exited bool
	msg := captureStderr(t, func() {
		code, exited = captureExit(t, func() { refuseSelfSpawn(exe) })
	})
	if !exited {
		t.Fatal("refuseSelfSpawn returned; it must die before exec.Command")
	}
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	for _, want := range []string{
		"refusing to spawn an angelus from a test binary",
		"cli.test",
		"mustConnectAngelus/ensureAngelus",
		"fork bomb",
		"inject an endpoint",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message must name the trap; missing %q in:\n%s", want, msg)
		}
	}
}

// TestRefuseSelfSpawnHonoursTheEnvSwitch covers harnesses whose binary is
// not named *.test — benchmarks under a scope, fuzzers, CI runners.
func TestRefuseSelfSpawnHonoursTheEnvSwitch(t *testing.T) {
	t.Setenv("FIGARO_NO_SELF_SPAWN", "1")
	var exited bool
	msg := captureStderr(t, func() {
		_, exited = captureExit(t, func() { refuseSelfSpawn("/usr/bin/figaro") })
	})
	if !exited {
		t.Fatal("FIGARO_NO_SELF_SPAWN must refuse even a real figaro path")
	}
	if !strings.Contains(msg, "FIGARO_NO_SELF_SPAWN is set") {
		t.Errorf("message should name the switch: %s", msg)
	}
}

// TestRealBinaryStillSpawns guards the other direction: the guard must not
// break the actual product. A figaro on a normal path passes through.
func TestRealBinaryStillSpawns(t *testing.T) {
	// Hermetic against the harness: the standing test recipe exports
	// FIGARO_NO_SELF_SPAWN=1, which would refuse every path by design.
	t.Setenv("FIGARO_NO_SELF_SPAWN", "")
	if _, exited := captureExit(t, func() { refuseSelfSpawn("/nix/store/abc-figaro/bin/figaro") }); exited {
		t.Error("a real figaro path must not be refused")
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns
// what was written. die() writes there before exiting, so this is how the
// message is inspected without a subprocess.
//
// The write end is closed BEFORE reading the result, not in a defer: `return
// <-done` evaluates the receive first and the defer only afterwards, so a
// deferred Close deadlocks against a reader waiting for EOF. (It did.)
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %s", err)
	}
	prev := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn() // captureExit recovers inside, so this returns normally

	os.Stderr = prev
	w.Close()
	out := <-done
	r.Close()
	return out
}

// TestMain is the second cut of the same wire, and it is deliberate
// redundancy: refuseSelfSpawn stops a parent from FORKING a test binary,
// while this stops a test binary that somehow got forked anyway from
// RUNNING the suite. Either alone breaks the chain; together, generation 1
// cannot become generation 2 even if a future path skips the guard.
//
// _FIGARO_DAEMON=1 is the flag cli.Run uses to become the angelus. It has
// no business in a test binary, and reaching TestMain with it set means the
// bomb already lit.
func TestMain(m *testing.M) {
	if os.Getenv("_FIGARO_DAEMON") == "1" {
		os.Stderr.WriteString(
			"cli tests: _FIGARO_DAEMON=1 in a test binary — refusing to run the suite.\n" +
				"  something exec'd this test binary as an angelus; that is the fork-bomb\n" +
				"  path (see refuseSelfSpawn in angelus_client.go). Aborting generation 1.\n")
		os.Exit(3)
	}
	os.Exit(m.Run())
}
