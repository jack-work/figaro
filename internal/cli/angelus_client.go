package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/transport"
)

// ariaBackend constructs the aria storage backend, rooted under
// stateDir() so FIGARO_STATE_DIR / XDG_STATE_HOME isolate it.
func ariaBackend() (store.Backend, error) {
	dir := filepath.Join(stateDir(), "arias")
	return store.NewFileBackend(dir)
}

func angelusRuntimeDir() string {
	// FIGARO_RUNTIME_DIR is an explicit override used as-is (no
	// "figaro" suffix appended) — lets dev shells point at an
	// isolated runtime without colliding with the user's daemon.
	if d := os.Getenv("FIGARO_RUNTIME_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "figaro")
	}
	return filepath.Join(os.TempDir(), "figaro")
}

func angelusSocketPath() string {
	return filepath.Join(angelusRuntimeDir(), "angelus.sock")
}

// ensureAngelus starts the angelus if needed.
func ensureAngelus() {
	sockPath := angelusSocketPath()
	ep := transport.UnixEndpoint(sockPath)
	if cli, err := angelus.DialClient(ep); err == nil {
		cli.Close()
		return
	}

	exe, err := os.Executable()
	if err != nil {
		die("find executable: %s", err)
	}

	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "_FIGARO_DAEMON=1")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		die("start angelus: %s", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cli, err := angelus.DialClient(ep); err == nil {
			cli.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	die("angelus did not start within 5 seconds")
}

func mustConnectAngelus(loaded *config.Loaded) *angelus.Client {
	_ = loaded
	ensureHush()
	ensureAngelus()
	ep := transport.UnixEndpoint(angelusSocketPath())
	cli, err := angelus.DialClient(ep)
	if err != nil {
		die("connect angelus: %s", err)
	}
	return cli
}

func mustCreateAndBind(ctx context.Context, acli *angelus.Client, loaded *config.Loaded, ppid int) (string, transport.Endpoint) {
	createResp, err := createWithFirstRun(ctx, loaded, func() (*rpc.CreateResponse, error) {
		return acli.Create(ctx, "", nil)
	})
	if err != nil {
		die("create figaro: %s", err)
	}

	if err := acli.Bind(ctx, ppid, createResp.FigaroID); err != nil {
		die("bind: %s", err)
	}

	ep := transport.Endpoint{
		Scheme:  createResp.Endpoint.Scheme,
		Address: createResp.Endpoint.Address,
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, serr := os.Stat(ep.Address); serr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	return createResp.FigaroID, ep
}
