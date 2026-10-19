package cli

import (
	"context"
	"fmt"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store/xwal"
)

// The daemon inherits the descriptor for its lifetime, so the startup log
// doubles as a daemon log; it is truncated only when no daemon answered.
func angelusStartupLog() string {
	return filepath.Join(boot.AngelusRuntimeDir(), "angelus.startup")
}

const startupDiagnosisLines = 8

// The daemon detaches, so its stderr is the only channel it has to explain a refusal.
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

func isTestBinary(exe string) bool { return boot.IsTestBinary(exe) }

func ensureAngelus() {
	if d := resolveDoor(); d.configured {
		cli, err := sdk.DialAngelus(d.ep)
		if err != nil {
			die("troupe %s: %s\n"+
				"  a troupe is configured, so figaro does not start a daemon", d.label, err)
		}
		checkTroupeBuild(cli, d.label)
		cli.Close()
		return
	}
	sockPath := boot.AngelusSocketPath()
	ep := transport.UnixEndpoint(sockPath)
	if cli, err := sdk.DialAngelus(ep); err == nil {
		checkDaemonBuild(cli)
		cli.Close()
		return
	}

	ensureHush()
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
	if err := os.MkdirAll(boot.AngelusRuntimeDir(), 0o700); err == nil {
		// Append, not truncate: several clients racing a cold start each
		// open this file.
		if f, err := os.OpenFile(angelusStartupLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			cmd.Stderr = f
			defer f.Close()
		}
	}
	if err := cmd.Start(); err != nil {
		die("start angelus: %s", err)
	}

	// The daemon is our child until we exit, so a fast failure is observable.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// A live daemon is never declared dead: the first open after an upgrade
	// migrates the store, and killing it mid-way destroys arias.
	start := time.Now()
	var deferred time.Time
	var lastNotice time.Time
	notified := false
	for {
		if cli, err := sdk.DialAngelus(ep); err == nil {
			cli.Close()
			if notified {
				fmt.Fprintln(stderrw, "angelus: ready")
			}
			return
		}
		select {
		case werr := <-exited:
			if werr != nil {
				die("angelus exited during startup (%v)%s", werr, startupDiagnosis())
			}
			// Exit 0 means it found an incumbent and stood down: keep
			// dialing for the incumbent.
			deferred = time.Now()
		default:
		}
		switch {
		case !deferred.IsZero() && time.Since(deferred) > incumbentGrace:
			die("angelus stood down for another instance that never answered on %s%s",
				boot.AngelusSocketPath(), startupDiagnosis())
		case time.Since(start) > startupHardCap:
			die("angelus has not answered in %s%s", startupHardCap, startupDiagnosis())
		}
		if time.Since(lastNotice) > startupNoticeEvery && time.Since(start) > startupNoticeAfter {
			lastNotice = time.Now()
			notified = true
			fmt.Fprintf(stderrw, "angelus: %s (waited %s; giving up at %s)\n",
				startupActivity(), time.Since(start).Round(time.Second), startupHardCap)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// startupActivity says only what it can check; NeedsFlatten is one file read.
func startupActivity() string {
	if need, err := xwal.NeedsFlatten(boot.AriaRoot()); err == nil && need {
		return "migrating the store layout: let it finish; interrupting a migration " +
			"is the one thing that can cost you arias"
	}
	return "still starting"
}

const (
	// After startupNoticeAfter we explain, and keep explaining every
	// startupNoticeEvery.
	startupNoticeAfter = 3 * time.Second
	startupNoticeEvery = 30 * time.Second
	startupHardCap     = 10 * time.Minute
	// incumbentGrace is how long we keep dialing after our own child stood down
	// for an existing daemon.
	incumbentGrace = 10 * time.Second
)

func mustConnectAngelus(loaded *config.Loaded) *sdk.Angelus {
	_ = loaded
	ensureAngelus()
	cli, err := sdk.DialAngelus(doorEndpoint())
	if err != nil {
		die("connect angelus: %s", err)
	}
	return cli
}

// mustCreate mints an aria and does not attend it: every birth verb says so
// with attendNew.
func mustCreate(ctx context.Context, acli *sdk.Angelus, loaded *config.Loaded, d dressing) (string, sdk.Addr) {
	createResp, err := createWithFirstRun(ctx, loaded, d, func() (*rpc.CreateResponse, error) {
		return acli.Create(ctx, d.names, d.patch)
	})
	if err != nil {
		dieWithClosure(err, "create figaro: %s", err)
	}
	return createResp.FigaroID, ariaAddr(createResp.FigaroID)
}

// attendNew moves the shell onto what a verb created for it; with binding
// disabled it is a silent no-op.
func attendNew(ctx context.Context, acli *sdk.Angelus, id string) {
	if err := bindBinding(ctx, acli, shellPID, id, 0); err != nil {
		die("attend %s: %s", id, err)
	}
}

// A daemon from another revision renders nothing rather than failing loudly, so
// the mismatch is named.
func checkDaemonBuild(cli *sdk.Angelus) {
	mine := boot.BuildRevision()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := cli.Status(ctx)
	if err != nil || st == nil {
		return
	}
	switch buildHandshake(st.Build, mine) {
	case handshakeOK:
		return
	case handshakeCLIUnknown:
		fmt.Fprintf(stderrw,
			"figaro: this CLI's build is unknown, so it cannot be checked against\n"+
				"        the running angelus (%s). If output is missing or garbled,\n"+
				"        run `figaro stop` and retry; see the tmux-testing skill to\n"+
				"        build a stamped binary.\n", short12(st.Build))
	case handshakeDaemonOld:
		fmt.Fprintf(stderrw,
			"figaro: the running angelus predates the build check, so it is older\n"+
				"        than this CLI (%s). If output is missing or garbled,\n"+
				"        run `figaro stop` and retry.\n", short12(mine))
	case handshakeMixedSchemes:
		fmt.Fprintf(stderrw,
			"figaro: this CLI and the running angelus name their builds differently,\n"+
				"        so they cannot be compared:\n"+
				"          daemon %s (%s)\n          cli    %s (%s)\n"+
				"        They may or may not be the same release. If output is missing\n"+
				"        or garbled, run `figaro stop`: the next command starts an\n"+
				"        angelus from THIS binary, and the pair matches by construction.\n",
			short12(st.Build), boot.BuildIdentityKind(st.Build), short12(mine), boot.BuildIdentityKind(mine))
	case handshakeRefuse:
		die("running angelus is a different build than this CLI:\n"+
			"  daemon %s\n  cli    %s\n"+
			"the wire changes between builds, so this pair would render nothing.\n"+
			"run `figaro stop` and retry.", short12(st.Build), short12(mine))
	}
}

func checkTroupeBuild(cli *sdk.Angelus, label string) {
	mine := boot.BuildRevision()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := cli.Status(ctx)
	if err != nil || st == nil {
		return
	}
	if buildHandshake(st.Build, mine) == handshakeRefuse {
		die("the daemon at %s is a different build than this CLI:\n"+
			"  daemon %s\n  cli    %s\n"+
			"the wire changes between builds; use a figaro of the daemon's build.", label, short12(st.Build), short12(mine))
	}
}

func buildHandshake(daemon, mine string) handshakeVerdict {
	switch {
	case daemon == mine:
		return handshakeOK
	case mine == "":
		return handshakeCLIUnknown
	case daemon == "":
		return handshakeDaemonOld
	case boot.BuildIdentityKind(daemon) != boot.BuildIdentityKind(mine):
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
