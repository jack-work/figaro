package gateway_test

// END TO END: a real angelus, a real gateway in front of it, and the real
// SDK client reaching the daemon THROUGH the tunnel.
//
// This is the test that matters. The unit tests prove the pump moves bytes;
// this one proves the bytes it moves are figaro's, that a method served by
// the daemon answers across the tunnel, and that the SDK needed no change to
// get there.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
)

// figaroBinary builds the CLI once for this test binary.
func figaroBinary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("FIGARO_TEST_BINARY"); p != "" {
		return p
	}
	out := filepath.Join(t.TempDir(), "figaro")
	cmd := exec.Command("go", "build", "-o", out, "github.com/jack-work/figaro/cmd/figaro")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build figaro for e2e: %v\n%s", err, b)
	}
	return out
}

// liveDaemon starts an angelus on an isolated runtime dir and returns its
// socket path.
func liveDaemon(t *testing.T, bin string) string {
	t.Helper()
	rt := t.TempDir()
	state := t.TempDir()

	cmd := exec.Command(bin, "--angelus")
	cmd.Env = append(os.Environ(),
		"FIGARO_RUNTIME_DIR="+rt,
		"FIGARO_STATE_DIR="+state,
		"_FIGARO_DAEMON=1",
	)
	var log strings.Builder
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start angelus: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	sock := filepath.Join(rt, "angelus.sock")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			// The file existing is not the same as the daemon answering.
			if cli, err := sdk.DialAngelus(transport.UnixEndpoint(sock)); err == nil {
				cli.Close()
				return sock
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Skipf("angelus never came up:\n%s", log.String())
	return ""
}

// TestCLIReachesDaemonThroughGateway is the whole claim in one function.
func TestCLIReachesDaemonThroughGateway(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	bin := figaroBinary(t)
	sock := liveDaemon(t, bin)

	// Talk to the daemon DIRECTLY first, so the comparison below is against
	// a known answer rather than an assumption.
	direct, err := sdk.DialAngelus(transport.UnixEndpoint(sock))
	if err != nil {
		t.Fatalf("dial angelus directly: %v", err)
	}
	defer direct.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	wantStatus, err := direct.Status(ctx)
	if err != nil {
		t.Fatalf("status direct: %v", err)
	}

	// Now stand a gateway in front and ask the same question through it.
	gwSock := filepath.Join(t.TempDir(), "gw.sock")
	gw := exec.Command(bin, "serve", "--listen", "unix://"+gwSock)
	gw.Env = append(os.Environ(),
		"FIGARO_RUNTIME_DIR="+filepath.Dir(sock),
		"FIGARO_NO_SELF_SPAWN=1",
	)
	var gwLog strings.Builder
	gw.Stdout, gw.Stderr = &gwLog, &gwLog
	if err := gw.Start(); err != nil {
		t.Fatalf("start gateway: %v", err)
	}
	t.Cleanup(func() {
		_ = gw.Process.Kill()
		_, _ = gw.Process.Wait()
	})

	waitFor(t, gwSock, &gwLog)

	// The SDK, unmodified, over a unix-socket HTTP tunnel.
	ep := transport.Endpoint{Scheme: "http", Address: "unix:" + gwSock}
	tun, err := sdk.DialAngelus(ep)
	if err != nil {
		t.Fatalf("dial through gateway: %v\ngateway log:\n%s", err, gwLog.String())
	}
	defer tun.Close()

	gotStatus, err := tun.Status(ctx)
	if err != nil {
		t.Fatalf("status through gateway: %v\ngateway log:\n%s", err, gwLog.String())
	}
	if gotStatus.Build != wantStatus.Build || gotStatus.Uptime == 0 {
		t.Fatalf("gateway answered a different daemon: %q vs %q",
			gotStatus.Build, wantStatus.Build)
	}

	// A second method, to prove it is a tunnel and not one lucky handler.
	if _, err := tun.List(ctx); err != nil {
		t.Fatalf("list through gateway: %v", err)
	}
}

func waitFor(t *testing.T, path string, log *strings.Builder) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never appeared:\n%s", path, log.String())
}
