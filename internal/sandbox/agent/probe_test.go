package agent

import (
	"os/exec"
	"strings"
	"testing"
)

var writable = []string{"/work", "/tmp", "/var/tmp", "/dev/shm"}

const clean = `self 40 7 1000
writable /work
writable /tmp
writable /var/tmp
writable /dev/shm
proc 1 0 0 systemd
proc 7 1 1000 figaro
proc 40 7 1000 bash
proc 41 40 1000 find
proc 52 1 0 systemd-journal
end
`

func TestAFreshSandboxPassesTheProbe(t *testing.T) {
	if err := ParseProbe(clean, writable); err != nil {
		t.Fatal(err)
	}
}

func TestTheProbeFindsWhatALeaseLeftBehind(t *testing.T) {
	for name, c := range map[string]struct{ out, want string }{
		"a file in /work":        {strings.Replace(clean, "end\n", "entry /work/canary\nend\n", 1), "/work/canary"},
		"a writable home":        {strings.Replace(clean, "end\n", "writable /home/box\nend\n", 1), "/home/box"},
		"a writable file in etc": {strings.Replace(clean, "end\n", "writable /etc/profile.local\nend\n", 1), "/etc/profile.local"},
		"a process left running": {strings.Replace(clean, "end\n", "proc 99 1 1000 sleep\nend\n", 1), "sleep"},
		"no end":                 {strings.Replace(clean, "end\n", "", 1), "to its end"},
		"no self":                {strings.Replace(clean, "self 40 7 1000\n", "", 1), "to its end"},
	} {
		err := ParseProbe(c.out, writable)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, c.want)
		}
	}
}

func TestTheProbeScriptParses(t *testing.T) {
	sh, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	cmd := exec.Command(sh, "-n", "-c", ProbeScript(append(writable, "/it's")))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
