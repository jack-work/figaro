package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/testfence"
)

// ensureAngelus re-execs os.Executable(); from a test binary that is a fork bomb.

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
		{"/home/gluck/some.test/figaro", false},
	}
	for _, tc := range cases {
		if got := isTestBinary(tc.exe); got != tc.want {
			t.Errorf("isTestBinary(%q) = %v, want %v", tc.exe, got, tc.want)
		}
	}
}

func TestRefuseSelfSpawnFromTestBinary(t *testing.T) {
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

func TestRealBinaryStillSpawns(t *testing.T) {
	t.Setenv("FIGARO_NO_SELF_SPAWN", "")
	if _, exited := captureExit(t, func() { refuseSelfSpawn("/nix/store/abc-figaro/bin/figaro") }); exited {
		t.Error("a real figaro path must not be refused")
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %s", err)
	}
	prev := os.Stderr
	os.Stderr = w
	prevStderrw := stderrw
	stderrw = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()

	os.Stderr = prev
	stderrw = prevStderrw
	w.Close()
	out := <-done
	r.Close()
	return out
}

func TestMain(m *testing.M) {
	if os.Getenv("_FIGARO_DAEMON") == "1" {
		os.Stderr.WriteString(
			"cli tests: _FIGARO_DAEMON=1 in a test binary: refusing to run the suite.\n" +
				"  something exec'd this test binary as an angelus; that is the fork-bomb\n" +
				"  path (see refuseSelfSpawn in angelus_client.go). Aborting generation 1.\n")
		os.Exit(3)
	}
	// No test may reach the OS keyring (a test vault that saves its passphrase
	// there can overwrite figaro:default), and none may inherit a dev shell's
	// vault, whose agent would re-execute the test binary.
	testfence.Install()
	code := m.Run()
	// A case's Cleanup has not run until m.Run returns.
	if !smokeParticipation() && code == 0 {
		code = 1
	}
	os.Exit(code)
}
