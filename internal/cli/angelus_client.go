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
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/transport"
	"github.com/jack-work/figaro/internal/store/xwal"
)

// ariaBackend constructs the XWAL aria tree under the configured state root,
// sized by `[store] segment_size` (config owns the default and its floor).
func ariaBackend(loaded *config.Loaded) (store.Backend, error) {
	root := ariaRoot()
	b, err := store.NewXwalBackend(root, loaded.SegmentSize())
	if err != nil {
		return nil, err
	}
	// The one place the trunk capability is decided. With it off, nothing
	// constructs a trunk pstate and the hierarchy is the fork topology.
	if err := wire.Install(b.Store(), root, wire.Capabilities{Trunks: loaded.Trunks()}); err != nil {
		return nil, err
	}
	return b, nil
}

func angelusRuntimeDir() string {
	// FIGARO_RUNTIME_DIR is an explicit override used as-is (no
	// "figaro" suffix appended): lets dev shells point at an
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
// quoted back: enough for a stack-free error, not a wall of slog.
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

// refuseSelfSpawn stops the bootstrap when the executable about to be
// forked is not figaro.
//
// ensureAngelus starts the daemon by re-executing os.Executable(), which in
// a test binary is `cli.test`: so a test touching any daemon-connecting
// path spawns a detached copy of the TEST BINARY, which re-runs the suite,
// which spawns another. On 2026-07-30 that put 1391 cli.test processes in
// the OOM task dump (45.8G + 50.2G swap) and took the desktop session with
// it. FIGARO_NO_SELF_SPAWN=1 arms the same refusal for harnesses whose
// binary is not named *.test.
func refuseSelfSpawn(exe string) {
	if envTruthy(os.Getenv("FIGARO_NO_SELF_SPAWN")) {
		die("refusing to spawn an angelus: FIGARO_NO_SELF_SPAWN is set\n" +
			"  a daemon-connecting path was reached under a harness that forbids it\n" +
			"  inject an endpoint instead of starting a daemon")
	}
	if isTestBinary(exe) {
		die("refusing to spawn an angelus from a test binary (%s)\n"+
			"  a test reached a daemon-connecting path (mustConnectAngelus/ensureAngelus)\n"+
			"  the daemon is started by re-executing os.Executable(), which here is the\n"+
			"  TEST BINARY: every spawn re-runs the suite and spawns again (fork bomb)\n"+
			"  tests must inject an endpoint, never reach the bootstrap", filepath.Base(exe))
	}
}

// isTestBinary reports whether exe looks like a `go test` binary. Go names
// them <pkg>.test (<pkg>.test.exe on Windows).
func isTestBinary(exe string) bool {
	base := strings.TrimSuffix(filepath.Base(exe), ".exe")
	return strings.HasSuffix(base, ".test")
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
	refuseSelfSpawn(exe)

	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "_FIGARO_DAEMON=1")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.SysProcAttr = detachAttr()
	// Best-effort: if we cannot open the log, fall back to the old
	// discard rather than refusing to start a daemon over it.
	if err := os.MkdirAll(angelusRuntimeDir(), 0o700); err == nil {
		// APPEND, not truncate. Several clients racing a cold start each
		// open this file, and O_CREATE|O_TRUNC let a loser wipe the log the
		// winner was writing into -- including the only trace a daemon
		// crash leaves.
		if f, err := os.OpenFile(angelusStartupLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			cmd.Stderr = f
			defer f.Close() // the child holds its own dup
		}
	}
	if err := cmd.Start(); err != nil {
		die("start angelus: %s", err)
	}

	// The daemon is still our child until we exit, so a fast failure is
	// observable: report it immediately instead of idling out the
	// deadline on a process that is already gone.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// A LIVE daemon is never declared dead. The old rule was a flat five
	// seconds, and the first open after an upgrade migrates the store
	// layout, which takes about five seconds on a real one: so the CLI
	// told the user his daemon had failed at the exact moment when killing
	// it would destroy his arias. Now: while the child is alive we keep
	// waiting and say what it is doing; only an exit, or a very long
	// silence, is a failure.
	start := time.Now()
	var deferred time.Time // when our child stood down for an incumbent
	var lastNotice time.Time
	notified := false
	for {
		if cli, err := angelus.DialClient(ep); err == nil {
			cli.Close()
			if notified {
				fmt.Fprintln(os.Stderr, "angelus: ready")
			}
			return
		}
		select {
		case werr := <-exited:
			if werr != nil {
				die("angelus exited during startup (%v)%s", werr, startupDiagnosis())
			}
			// Exit 0 means it found an incumbent and stood down. That is
			// the ordinary outcome of several clients starting at once,
			// not an error: keep dialing for the incumbent instead of
			// reporting a failure with a nil cause.
			deferred = time.Now()
		default:
		}
		switch {
		case !deferred.IsZero() && time.Since(deferred) > incumbentGrace:
			die("angelus stood down for another instance that never answered on %s%s",
				angelusSocketPath(), startupDiagnosis())
		case time.Since(start) > startupHardCap:
			die("angelus has not answered in %s%s", startupHardCap, startupDiagnosis())
		}
		if time.Since(lastNotice) > startupNoticeEvery && time.Since(start) > startupNoticeAfter {
			lastNotice = time.Now()
			notified = true
			fmt.Fprintf(os.Stderr, "angelus: %s (waited %s; giving up at %s)\n",
				startupActivity(), time.Since(start).Round(time.Second), startupHardCap)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ariaRoot is the aria store's directory. ONE spelling, because the
// startup notice is only truthful while every caller agrees on it, and
// the notice is the one that would drift silently -- it degrades to
// "still starting" with no error.
func ariaRoot() string { return filepath.Join(stateDir(), "arias") }

// startupActivity says what the daemon is doing, and only says the thing
// it can check. Asserting a migration that is not happening is what sends
// a user to pkill -- a wrong explanation is worse than none. NeedsFlatten
// is one file read.
func startupActivity() string {
	if need, err := xwal.NeedsFlatten(ariaRoot()); err == nil && need {
		return "migrating the store layout: let it finish; interrupting a migration " +
			"is the one thing that can cost you arias"
	}
	return "still starting"
}

const (
	// startupNoticeAfter is when we start explaining rather than waiting
	// silently, and startupNoticeEvery keeps explaining: one hopeful
	// sentence followed by ten minutes of silence reads exactly like the
	// hang the sentence told the user not to interrupt.
	//
	// startupHardCap is the only thing that can call a LIVE daemon a
	// failure, and it is deliberately far past any migration measured
	// (about 5s for 483 nodes) so that a bigger store is slow rather than
	// broken. Every notice names it, so the silence has a stated end.
	startupNoticeAfter = 3 * time.Second
	startupNoticeEvery = 30 * time.Second
	startupHardCap     = 10 * time.Minute
	// incumbentGrace is how long we keep dialing after our own child stood
	// down for an existing daemon. Short: the incumbent is already up, or
	// it is not.
	incumbentGrace = 10 * time.Second
)

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

// mustCreateAndBindOutfit mints an aria and binds this shell to it. An empty
// dressing means "as usual": the angelus folds the configured default.
func mustCreateAndBindOutfit(ctx context.Context, acli *angelus.Client, loaded *config.Loaded, ppid int, d dressing) (string, transport.Endpoint) {
	createResp, err := createWithFirstRun(ctx, loaded, d, func() (*rpc.CreateResponse, error) {
		return acli.Create(ctx, d.names, d.patch)
	})
	if err != nil {
		dieWithClosure(err, "create figaro: %s", err)
	}

	if err := bindBinding(ctx, acli, ppid, createResp.FigaroID, 0); err != nil {
		die("bind: %s", err)
	}

	ep := transport.Endpoint{
		Scheme:  createResp.Endpoint.Scheme,
		Address: createResp.Endpoint.Address,
	}

	if err := waitForSocket(ep.Address, 3*time.Second); err != nil {
		dieWithClosure(err, "create figaro: %s", err)
	}

	return createResp.FigaroID, ep
}

// checkDaemonBuild refuses to speak to a daemon built from a different
// revision. The wire shape changes between builds, so a mismatched pair does
// not fail loudly: it renders NOTHING, which reads as a broken terminal
// rather than a stale process. Naming both revisions turns an hour of
// confusion into one command.
//
// An unknown revision must not be treated as mismatched: but it must not be
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
	switch buildHandshake(st.Build, mine) {
	case handshakeOK:
		return
	case handshakeCLIUnknown:
		// We cannot prove incompatibility, but silence is the failure mode we
		// are here to kill: warn loudly rather than let the user stare at an
		// empty screen.
		fmt.Fprintf(os.Stderr,
			"figaro: this CLI's build is unknown, so it cannot be checked against\n"+
				"        the running angelus (%s). If output is missing or garbled,\n"+
				"        run `figaro stop` and retry; see the tmux-testing skill to\n"+
				"        build a stamped binary.\n", short12(st.Build))
	case handshakeDaemonOld:
		fmt.Fprintf(os.Stderr,
			"figaro: the running angelus predates the build check, so it is older\n"+
				"        than this CLI (%s). If output is missing or garbled,\n"+
				"        run `figaro stop` and retry.\n", short12(mine))
	case handshakeMixedSchemes:
		fmt.Fprintf(os.Stderr,
			"figaro: this CLI and the running angelus name their builds differently,\n"+
				"        so they cannot be compared:\n"+
				"          daemon %s (%s)\n          cli    %s (%s)\n"+
				"        They may or may not be the same release. If output is missing\n"+
				"        or garbled, run `figaro stop`: the next command starts an\n"+
				"        angelus from THIS binary, and the pair matches by construction.\n",
			short12(st.Build), buildIdentityKind(st.Build), short12(mine), buildIdentityKind(mine))
	case handshakeRefuse:
		die("running angelus is a different build than this CLI:\n"+
			"  daemon %s\n  cli    %s\n"+
			"the wire changes between builds, so this pair would render nothing.\n"+
			"run `figaro stop` and retry.", short12(st.Build), short12(mine))
	}
}

// buildHandshake is the whole decision, pure so it can be tested as a matrix
// rather than through a daemon.
//
// The rule is COMPARE LIKE WITH LIKE. A source build reports a git revision; a
// `go install <module>@vX.Y.Z` reports the module version, because the proxy
// ships a zip with no VCS metadata. Neither converts into the other, so across
// schemes a difference proves nothing, a nix daemon beside a proxy CLI of the
// SAME release can never compare equal.
//
// Refusing there would brick a legitimate pair with no path back: the user's
// only tools are the two binaries now refusing each other. Within a scheme a
// difference is real and the wire may differ, so it still refuses. `figaro
// stop` dials the socket directly (system.go) and does not consult this check,
// which is what keeps the remedy reachable in every branch below.
func buildHandshake(daemon, mine string) handshakeVerdict {
	switch {
	case daemon == mine:
		return handshakeOK // matched, or both unknown
	case mine == "":
		return handshakeCLIUnknown
	case daemon == "":
		return handshakeDaemonOld
	case buildIdentityKind(daemon) != buildIdentityKind(mine):
		return handshakeMixedSchemes
	default:
		return handshakeRefuse
	}
}

type handshakeVerdict int

const (
	handshakeOK handshakeVerdict = iota
	handshakeCLIUnknown
	handshakeDaemonOld
	handshakeMixedSchemes
	handshakeRefuse
)

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
