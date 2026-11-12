package cli

import (
	"context"
	"fmt"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/figwal/xwal"
	"github.com/jack-work/figaro/internal/config"
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
	refuseOffTape("the daemon")
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

	if err := boot.SpawnRefusal(os.Getenv); err != nil {
		die("%s", err)
	}
	h := MustHush()
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
	gate, err := gateOnHush(cmd, h)
	if err != nil {
		die("start angelus: %s", err)
	}
	if err := cmd.Start(); err != nil {
		gate.decide(err)
		die("start angelus: %s", err)
	}
	hushReady := gate.open(h)
	if placement, ok := placeAngelus(os.Getenv, ownManagerSocket, boot.AngelusRuntimeDir()); ok {
		if _, err := moveIntoScope(placement, cmd.Process.Pid); err != nil && cmd.Stderr != nil {
			fmt.Fprintf(cmd.Stderr, "angelus: left in the caller's cgroup, so it ends with the caller's unit: %s\n", err)
		}
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
		select {
		case err := <-hushReady:
			if err != nil {
				dieHush(err)
			}
			hushReady = nil
		default:
		}
		if cli, err := sdk.DialAngelus(ep); err == nil {
			cli.Close()
			if hushReady != nil {
				if err := <-hushReady; err != nil {
					dieHush(err)
				}
			}
			if notified {
				fmt.Fprintln(stderrw, "angelus: ready")
			}
			return
		}
		select {
		case werr := <-exited:
			if hushReady != nil {
				if err := <-hushReady; err != nil {
					dieHush(err)
				}
				hushReady = nil
			}
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
			die("angelus stood down for another instance that never answered on %s%s%s",
				boot.AngelusSocketPath(), storeOwnerHint(), startupDiagnosis())
		case time.Since(start) > startupHardCap:
			die("angelus has not answered in %s%s", startupHardCap, startupDiagnosis())
		}
		if hushReady == nil && time.Since(lastNotice) > startupNoticeEvery && time.Since(start) > startupNoticeAfter {
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
	if !bindingDisabled() && throughABox(ctx, acli) {
		fmt.Fprintf(stderrw, "figaro: a box does not attend shells; address %s with --id %s\n", ariaName(id), id)
		return
	}
	if err := bindBinding(ctx, acli, shellPID, id, 0); err != nil {
		die("attend %s: %s", id, err)
	}
}

// A daemon from another revision renders nothing rather than failing loudly, so
// the mismatch is named.
func checkDaemonBuild(cli *sdk.Angelus) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := cli.Status(ctx)
	if err != nil || st == nil {
		return
	}
	d := identityOf(st)
	mine := boot.BuildRevision()
	switch v := buildHandshake(d, mine, boot.BuildRelease()); v {
	case handshakeOK:
	case handshakeOtherMinor:
		warnOtherMinor("the running angelus", d)
	case handshakeCLIUnknown:
		fmt.Fprintf(stderrw,
			"figaro: this CLI's build is unknown, so it cannot be checked against\n"+
				"        the running angelus (%s). If output is missing or garbled,\n"+
				"        run `figaro stop` and retry; see the tmux-testing skill to\n"+
				"        build a stamped binary.\n", short12(d.build))
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
			short12(d.build), boot.BuildIdentityKind(d.build), short12(mine), boot.BuildIdentityKind(mine))
	case handshakeProtocolMismatch:
		die("the running angelus speaks protocol %d and this CLI speaks %d:\n"+
			"  daemon %s\n  cli    %s\n"+
			"run `figaro stop` and retry.", d.protocol, rpc.ProtocolVersion, describeBuild(d.build, d.release), describeBuild(mine, boot.BuildRelease()))
	default:
		die("running angelus is a different build than this CLI:\n"+
			"  daemon %s\n  cli    %s\n"+
			"it reports no protocol, so the pair cannot be checked.\n"+
			"run `figaro stop` and retry.", short12(d.build), short12(mine))
	}
	sawDaemon("the running angelus", d, mine)
}

func checkTroupeBuild(cli *sdk.Angelus, label string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := cli.Status(ctx)
	if err != nil || st == nil {
		return
	}
	d := identityOf(st)
	mine := boot.BuildRevision()
	switch buildHandshake(d, mine, boot.BuildRelease()) {
	case handshakeProtocolMismatch:
		die("the daemon at %s speaks protocol %d and this CLI speaks %d:\n"+
			"  daemon %s\n  cli    %s\n"+
			"use a figaro of the daemon's release line.", label, d.protocol, rpc.ProtocolVersion,
			describeBuild(d.build, d.release), describeBuild(mine, boot.BuildRelease()))
	case handshakeRefuse:
		die("the daemon at %s is a different build than this CLI:\n"+
			"  daemon %s\n  cli    %s\n"+
			"it reports no protocol, so the pair cannot be checked; use a figaro of the daemon's build.",
			label, short12(d.build), short12(mine))
	case handshakeOtherMinor:
		warnOtherMinor("the daemon at "+label, d)
	}
	sawDaemon("the daemon at "+label, d, mine)
}

type daemonIdentity struct {
	build    string
	release  string
	protocol int
	caps     []rpc.Capability
}

func identityOf(st *rpc.StatusResponse) daemonIdentity {
	return daemonIdentity{build: st.Build, release: st.Release, protocol: st.Protocol, caps: st.Capabilities}
}

func buildHandshake(d daemonIdentity, mine, mineRelease string) handshakeVerdict {
	switch {
	case d.build == mine:
		return handshakeOK
	case d.protocol != 0 && d.protocol != rpc.ProtocolVersion:
		return handshakeProtocolMismatch
	case d.protocol != 0 && d.release != "" && mineRelease != "":
		if releaseMinor(d.release) == releaseMinor(mineRelease) {
			return handshakeOK
		}
		return handshakeOtherMinor
	case mine == "":
		return handshakeCLIUnknown
	case d.build == "":
		return handshakeDaemonOld
	case boot.BuildIdentityKind(d.build) != boot.BuildIdentityKind(mine):
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
	handshakeProtocolMismatch
	handshakeOtherMinor
)

func releaseMinor(release string) string {
	parts := strings.SplitN(strings.TrimPrefix(release, "v"), ".", 3)
	if len(parts) < 2 {
		return release
	}
	return parts[0] + "." + parts[1]
}

func describeBuild(build, release string) string {
	if release == "" {
		return short12(build)
	}
	return release + " (" + short12(build) + ")"
}

func warnOtherMinor(who string, d daemonIdentity) {
	fmt.Fprintf(stderrw,
		"figaro: %s is %s and this CLI is %s: another minor release.\n"+
			"        They share protocol %d, so the pair works; features newer than\n"+
			"        the older side are refused by name, not dropped.\n",
		who, d.release, boot.BuildRelease(), d.protocol)
}

var daemonSeen struct {
	sync.Mutex
	known bool
	same  bool
	who   string
	d     daemonIdentity
	needs map[rpc.Capability]string
}

func sawDaemon(who string, d daemonIdentity, mine string) {
	recordDaemon(who, d, mine)
	if err := settlePendingNeeds(); err != nil {
		die("%s", err)
	}
}

func recordDaemon(who string, d daemonIdentity, mine string) {
	daemonSeen.Lock()
	defer daemonSeen.Unlock()
	daemonSeen.known, daemonSeen.who, daemonSeen.d = true, who, d
	daemonSeen.same = d.build == mine
	sdk.DescribePeerBuilds(seenBuilds)
}

func seenBuilds() string {
	daemonSeen.Lock()
	defer daemonSeen.Unlock()
	if !daemonSeen.known {
		return ""
	}
	return fmt.Sprintf("%s is another build than this CLI:\n  daemon %s\n  cli    %s",
		daemonSeen.who, describeBuild(daemonSeen.d.build, daemonSeen.d.release),
		describeBuild(boot.BuildRevision(), boot.BuildRelease()))
}

func settlePendingNeeds() error {
	daemonSeen.Lock()
	pending := daemonSeen.needs
	daemonSeen.needs = nil
	daemonSeen.Unlock()
	for c, use := range pending {
		if err := lacksForUse(c, use); err != nil {
			return err
		}
	}
	return nil
}

func needCapability(c rpc.Capability, use string) error {
	daemonSeen.Lock()
	if !daemonSeen.known {
		if daemonSeen.needs == nil {
			daemonSeen.needs = map[rpc.Capability]string{}
		}
		daemonSeen.needs[c] = use
		daemonSeen.Unlock()
		return nil
	}
	daemonSeen.Unlock()
	return lacksForUse(c, use)
}

func daemonLacks(c rpc.Capability) bool {
	daemonSeen.Lock()
	defer daemonSeen.Unlock()
	return daemonSeen.known && !daemonSeen.same && !slices.Contains(daemonSeen.d.caps, c)
}

func lacksForUse(c rpc.Capability, use string) error {
	if !daemonLacks(c) {
		return nil
	}
	daemonSeen.Lock()
	defer daemonSeen.Unlock()
	return fmt.Errorf("%s (%s) cannot take %s: it does not report %q; upgrade it, or use a figaro of its release",
		daemonSeen.who, describeBuild(daemonSeen.d.build, daemonSeen.d.release), use, c)
}

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func storeOwnerHint() string {
	pid, held := boot.StoreLockOwner()
	if !held || pid <= 0 {
		return ""
	}
	return fmt.Sprintf("\n  pid %d holds the store; `figaro stop` stops it", pid)
}

func throughABox(ctx context.Context, acli *sdk.Angelus) bool {
	hello, err := acli.Hello(ctx)
	return err == nil && hello.Principal.Kind == rpc.PrincipalSandbox
}
