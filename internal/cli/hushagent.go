package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jack-work/hush/client"
	"github.com/jack-work/hush/managed"
)

// The hush agent is the daemon's dependent, and nothing used to retire it.
//
// hush/managed spawns an EMBEDDED agent by re-exec'ing the consuming binary
// with HUSH_AGENT_CHILD=1, and its contract is that the agent OUTLIVES the
// process that spawned it: a sound default for hush, whose agent is shared
// across consumers and across upgrades. figaro's is not shared. It is scoped
// to figaro's own AppName, it is spawned by the daemon, it is kept alive by
// the daemon (keepHushAlive), and when the daemon is gone nothing will ever
// ask it anything again. It then sits on its socket holding an unlocked
// identity until its TTL, which figaro sets to 24 hours.
//
// So retiring it is figaro's policy, not hush's, and `figaro stop` is where
// it belongs.
//
// WHICH agent is the whole difficulty. Every figaro on the machine is the
// same executable with the same argv; a dev shell's agent, the user's real
// one and this one are distinguishable only by the socket they answer. And
// the socket a CLI would DERIVE from its own environment is not necessarily
// the one the daemon it is stopping actually has: FIGARO_HUSH_APP and
// FIGARO_HUSH_DIR are shell state, and a `figaro stop` typed in the wrong
// shell would then reach into another figaro's vault. So the daemon
// ADVERTISES the agent it owns, and stop reads that claim. The claim is a
// fact recorded by the only process that knows it, rather than a guess
// reconstructed by a process that does not.

// hushClaimPath is where a daemon advertises the embedded hush agent it
// owns: one line, the absolute path of that agent's socket. It sits beside
// angelus.pid, in the runtime dir, so it is scoped exactly the way the
// daemon is.
//
// Its EXISTENCE is the claim "this agent is mine to stop". A daemon using an
// external hush (the user's own `hush up`) writes nothing, and stop therefore
// has nothing to act on, which is the correct answer rather than a check
// somebody has to remember to write.
func hushClaimPath() string {
	return filepath.Join(angelusRuntimeDir(), "hush-agent")
}

// claimHushAgent records the socket of the embedded agent this daemon owns.
//
// It takes the mode and the runtime dir rather than a *managed.Hush so the
// external case is reachable from a test: constructing a managed.Hush in
// external mode requires a live external agent, which a test may not have,
// and the one branch that must never be wrong is exactly the one that would
// then go unexercised.
func claimHushAgent(mode managed.Mode, hushRuntimeDir string) error {
	if mode != managed.ModeEmbedded {
		// An external agent belongs to the user or to a service manager.
		// Killing it is not ours to do.
		return nil
	}
	if hushRuntimeDir == "" {
		return errors.New("hush claim: empty runtime dir")
	}
	sock := hushAgentSocket(hushRuntimeDir)
	if err := os.MkdirAll(angelusRuntimeDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(hushClaimPath(), []byte(sock+"\n"), 0o600)
}

// hushAgentSocket is where hush/managed puts an agent's socket for a given
// runtime dir. It mirrors managed.setupEmbedded, which joins "agent.sock"
// onto the resolved RuntimeDir; hush exposes the dir (via Config) but not
// the assembled path.
func hushAgentSocket(runtimeDir string) string {
	return filepath.Join(runtimeDir, "agent.sock")
}

// retireHushAgent shuts down the agent the daemon claimed, and drops the
// claim. It reports the socket it acted on and whether an agent was actually
// there to stop.
//
// Call it only once the daemon is CONFIRMED GONE. keepHushAlive respawns the
// agent on a ticker, so retiring it under a live daemon buys a few seconds of
// nothing.
//
// A claim naming a socket nobody answers is the ordinary case after a daemon
// was killed rather than stopped: hush reports that as ENOENT or
// ECONNREFUSED, and both mean the outcome we wanted. Anything else is
// reported, because a vault that refuses to shut down is worth a line on
// stderr.
func retireHushAgent() (sock string, stopped bool, err error) {
	raw, readErr := os.ReadFile(hushClaimPath())
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, readErr
	}
	sock = strings.TrimSpace(string(raw))
	if sock == "" {
		os.Remove(hushClaimPath())
		return "", false, nil
	}

	shutErr := client.NewWithSocket(sock).Shutdown()
	// The claim is spent either way: a live agent is now stopping, and a
	// dead one is not coming back.
	os.Remove(hushClaimPath())

	switch {
	case shutErr == nil:
		return sock, true, nil
	case hushAgentAlreadyGone(shutErr):
		return sock, false, nil
	default:
		return sock, false, shutErr
	}
}

// hushAgentAlreadyGone reports whether a shutdown failed because there was
// nothing left to shut down. managed keeps its own copy of this judgement
// unexported, so figaro states it here: a missing socket file, or one no
// process is listening on.
func hushAgentAlreadyGone(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// reportRetiredHushAgent runs the retirement and says what happened, in the
// same voice as the rest of `figaro stop`. Silence when there was no claim:
// most users have never heard of the agent and do not need to.
func reportRetiredHushAgent() {
	sock, stopped, err := retireHushAgent()
	switch {
	case err != nil:
		fmt.Fprintf(stderrw, "hush agent at %s did not shut down: %s\n", sock, err)
	case stopped:
		fmt.Fprintln(stderrw, "hush agent retired")
	}
}
