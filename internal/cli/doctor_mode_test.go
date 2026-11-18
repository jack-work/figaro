package cli

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorModeReadsTheDaemonsUidOffItsSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "angelus.sock")
	if r := judgeMode(sock, os.Getuid(), true); r.Mode != "home" || r.DaemonUID != -1 {
		t.Fatalf("no socket, self-spawning CLI: %+v", r)
	}
	if r := judgeMode(sock, os.Getuid(), false); r.Mode != "unknown" {
		t.Fatalf("no socket, no self-spawn: %+v", r)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if r := judgeMode(sock, os.Getuid(), false); r.Mode != "home" || r.DaemonUID != os.Getuid() {
		t.Fatalf("my socket: %+v", r)
	}
	if r := judgeMode(sock, os.Getuid()+1, false); r.Mode != "house" || r.DaemonUID != os.Getuid() {
		t.Fatalf("another uid's socket: %+v", r)
	}
	if p := probeSocket("b", sock); !p.Answers {
		t.Fatalf("a listening socket does not answer: %+v", p)
	}
	if p := probeSocket("b", sock+".gone"); p.Answers || p.Error == "" {
		t.Fatalf("a missing socket answers: %+v", p)
	}
}
