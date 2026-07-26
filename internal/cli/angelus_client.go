package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/transport"
)

// ariaBackend constructs the XWAL aria tree under the configured state root,
// sized by `[store] segment_size` (config owns the default and its floor).
func ariaBackend(loaded *config.Loaded) (store.Backend, error) {
	return store.NewXwalBackend(filepath.Join(stateDir(), "arias"), loaded.SegmentSize())
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

// angelusStartupLog is where a detaching angelus writes its early stderr.
// The daemon inherits the descriptor for its lifetime, so it doubles as a
// daemon log; ensureAngelus only truncates it when no daemon answered.
func angelusStartupLog() string {
	return filepath.Join(angelusRuntimeDir(), "angelus.startup")
}

// startupDiagnosisLines bounds how much of the daemon's early output is
// quoted back — enough for a stack-free error, not a wall of slog.
const startupDiagnosisLines = 8

// startupDiagnosis quotes whatever the daemon managed to say before it
// failed. Without it a deliberate refusal (the schema gate, a corrupt
// store) is indistinguishable from a hang: the daemon detaches, so its
// stderr is the only channel it has to explain itself.
func startupDiagnosis() string {
	b, err := os.ReadFile(angelusStartupLog())
	if err != nil {
		return ""
	}
	text := strings.TrimRight(string(b), "\n")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > startupDiagnosisLines {
		lines = lines[len(lines)-startupDiagnosisLines:]
	}
	return ":\n  " + strings.Join(lines, "\n  ")
}

// ensureAngelus starts the angelus if needed.
func ensureAngelus() {
	sockPath := angelusSocketPath()
	ep := transport.UnixEndpoint(sockPath)
	if cli, err := angelus.DialClient(ep); err == nil {
		checkDaemonBuild(cli)
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
	cmd.SysProcAttr = detachAttr()
	// Best-effort: if we cannot open the log, fall back to the old
	// discard rather than refusing to start a daemon over it.
	if err := os.MkdirAll(angelusRuntimeDir(), 0o700); err == nil {
		if f, err := os.Create(angelusStartupLog()); err == nil {
			cmd.Stderr = f
			defer f.Close() // the child holds its own dup
		}
	}
	if err := cmd.Start(); err != nil {
		die("start angelus: %s", err)
	}

	// The daemon is still our child until we exit, so a fast failure is
	// observable — report it immediately instead of idling out the
	// deadline on a process that is already gone.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if cli, err := angelus.DialClient(ep); err == nil {
			cli.Close()
			return
		}
		select {
		case werr := <-exited:
			die("angelus exited during startup (%v)%s", werr, startupDiagnosis())
		default:
		}
		if !time.Now().Before(deadline) {
			die("angelus did not start within 5 seconds%s", startupDiagnosis())
		}
		time.Sleep(50 * time.Millisecond)
	}
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
	return mustCreateAndBindLoadout(ctx, acli, loaded, ppid, "")
}

// mustCreateAndBindLoadout is mustCreateAndBind with an explicit loadout
// name. Empty string means "use the configured default_loadout" (angelus
// resolves it server-side).
func mustCreateAndBindLoadout(ctx context.Context, acli *angelus.Client, loaded *config.Loaded, ppid int, loadout string) (string, transport.Endpoint) {
	createResp, err := createWithFirstRun(ctx, loaded, func() (*rpc.CreateResponse, error) {
		return acli.Create(ctx, loadout, nil)
	})
	if err != nil {
		die("create figaro: %s", err)
	}

	if err := bindBinding(ctx, acli, ppid, createResp.FigaroID, 0); err != nil {
		die("bind: %s", err)
	}

	ep := transport.Endpoint{
		Scheme:  createResp.Endpoint.Scheme,
		Address: createResp.Endpoint.Address,
	}

	if err := waitForSocket(ep.Address, 3*time.Second); err != nil {
		die("create figaro: %s", err)
	}

	return createResp.FigaroID, ep
}

// checkDaemonBuild refuses to speak to a daemon built from a different
// revision. The wire shape changes between builds, so a mismatched pair does
// not fail loudly — it renders NOTHING, which reads as a broken terminal
// rather than a stale process. Naming both revisions turns an hour of
// confusion into one command.
//
// An unknown revision must not be treated as mismatched — but it must not be
// treated as fine either. A plain `go build` in a git worktree stamps nothing
// (Go auto-detects VCS only when .git is a directory), which is exactly how
// this project is developed, so unknown is the COMMON case: when one side is
// unknown and the other is not we warn. Only unknown-vs-unknown is silent,
// because then there is nothing provable to say.
func checkDaemonBuild(cli *angelus.Client) {
	mine := buildRevision()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := cli.Status(ctx)
	if err != nil || st == nil {
		return // transient: not worth blocking on
	}
	switch {
	case st.Build == mine:
		return // matched, or both unknown
	case mine == "":
		// We cannot prove incompatibility, but silence is the failure mode we
		// are here to kill: warn loudly rather than let the user stare at an
		// empty screen.
		fmt.Fprintf(os.Stderr,
			"figaro: this CLI's build is unknown, so it cannot be checked against\n"+
				"        the running angelus (%s). If output is missing or garbled,\n"+
				"        run `figaro stop` and retry; see skills/tmux-testing.md to\n"+
				"        build a stamped binary.\n", short12(st.Build))
		return
	case st.Build == "":
		// The daemon predates this check, so it is necessarily older than this
		// binary.
		fmt.Fprintf(os.Stderr,
			"figaro: the running angelus predates the build check, so it is older\n"+
				"        than this CLI (%s). If output is missing or garbled,\n"+
				"        run `figaro stop` and retry.\n", short12(mine))
		return
	}
	die("running angelus is a different build than this CLI:\n"+
		"  daemon %s\n  cli    %s\n"+
		"the wire changes between builds, so this pair would render nothing.\n"+
		"run `figaro stop` and retry.", short12(st.Build), short12(mine))
}

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
