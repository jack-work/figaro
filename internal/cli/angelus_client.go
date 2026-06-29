package cli

import (
	"context"
	"log/slog"
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

// ariaBackend constructs the aria storage backend (the XwalBackend fork
// tree), rooted under stateDir() so FIGARO_STATE_DIR / XDG_STATE_HOME
// isolate it. A pre-fork-tree (legacy FileBackend) store is backed up to
// a timestamped sibling and a fresh tree is started — no dual-read.
func ariaBackend() (store.Backend, error) {
	dir := filepath.Join(stateDir(), "arias")
	if err := backupLegacyAriaDir(dir); err != nil {
		return nil, err
	}
	return store.NewXwalBackend(dir)
}

// backupLegacyAriaDir renames a legacy aria store (per-aria subdirs, no
// index.json) out of the way so a fresh fork tree can take its place.
// No-op for an existing tree (index.json present), a fresh/absent dir,
// or an empty dir.
func backupLegacyAriaDir(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "index.json")); err == nil {
		return nil // already a fork tree
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil // fresh
	}
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil // empty; nothing to preserve
	}
	bak := dir + ".legacy-" + time.Now().Format("20060102-150405")
	if err := os.Rename(dir, bak); err != nil {
		if os.IsNotExist(err) {
			return nil // raced another process that already moved it
		}
		return err
	}
	slog.Warn("backed up legacy aria store; starting a fresh fork tree", "from", dir, "to", bak)
	return nil
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

	if err := acli.Bind(ctx, ppid, createResp.FigaroID, 0); err != nil {
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
