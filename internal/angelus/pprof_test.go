package angelus

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMemStatusReportsCounters(t *testing.T) {
	a := New(Config{RuntimeDir: t.TempDir()})

	st := a.MemStatus()
	if st == nil {
		t.Fatal("MemStatus returned nil")
	}
	if st.Goroutines <= 0 {
		t.Errorf("goroutines = %d, want > 0", st.Goroutines)
	}
	if st.HeapAllocBytes == 0 || st.SysBytes == 0 {
		t.Errorf("heap numbers look unread: alloc=%d sys=%d", st.HeapAllocBytes, st.SysBytes)
	}
	if st.MemLimitBytes <= 0 {
		t.Errorf("mem limit = %d, want a positive ceiling or MaxInt64", st.MemLimitBytes)
	}
	if st.LiveArias != 0 || st.Sessions != 0 {
		t.Errorf("fresh angelus reports live=%d sessions=%d, want 0/0", st.LiveArias, st.Sessions)
	}
	if st.PprofSocket != "" {
		t.Errorf("pprof reported as armed (%q) without StartPprof", st.PprofSocket)
	}
}

// A session belongs to the daemon, so the daemon can count it — this is
// the number that says whether hibernation is leaking children.
func TestMemStatusCountsSessions(t *testing.T) {
	a := New(Config{RuntimeDir: t.TempDir()})
	s := a.Sessions.Create("aria-a", "sleep 30")
	t.Cleanup(func() { a.Sessions.KillScope("aria-a") })

	if got := a.MemStatus().Sessions; got != 1 {
		t.Fatalf("sessions = %d, want 1 (created %s)", got, s.ID)
	}
}

// Profiling is opt-in. A daemon that armed it without being asked would
// be exposing pprof's handlers for the rest of its life.
func TestStartPprofOffByDefault(t *testing.T) {
	t.Setenv(PprofEnv, "")
	a := New(Config{RuntimeDir: t.TempDir()})

	if err := a.StartPprof(context.Background()); err != nil {
		t.Fatalf("StartPprof unarmed returned %v, want nil", err)
	}
	if _, err := os.Stat(a.PprofSocketPath()); !os.IsNotExist(err) {
		t.Fatalf("pprof socket exists without %s set", PprofEnv)
	}
	if a.MemStatus().PprofSocket != "" {
		t.Fatal("MemStatus advertises a profiler that was never armed")
	}
}

func TestStartPprofServesWhenArmed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(PprofEnv, "1")
	a := New(Config{RuntimeDir: dir})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.StartPprof(ctx); err != nil {
		t.Fatalf("StartPprof: %v", err)
	}

	sock := filepath.Join(dir, PprofSocketName)
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("pprof socket: %v", err)
	}
	// Same trust boundary as angelus.sock: owner only.
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("pprof socket mode = %o, want 600", perm)
	}
	if got := a.MemStatus().PprofSocket; got != sock {
		t.Errorf("MemStatus pprof = %q, want %q", got, sock)
	}

	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatalf("dial pprof: %v", err)
	}
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("GET /debug/pprof/heap?debug=1 HTTP/1.0\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 15)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	conn.Close()
	if string(buf[:9]) != "HTTP/1.0 " || string(buf[9:12]) != "200" {
		t.Fatalf("pprof responded %q, want a 200", buf)
	}
}
