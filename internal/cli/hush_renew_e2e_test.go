package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hush "github.com/jack-work/hush/client"
)

// Issue #46: the agent's TTL is absolute, and a keep-alive that only respawned
// a dead agent left the socket empty from the deadline to its next tick. The
// daemon must replace the agent before the deadline, so the socket is never
// empty for longer than one replacement takes.
//
//	FIGARO_HUSH_E2E=1 go test ./internal/cli -run TestHushAgentIsRenewed -count=1 -v
func TestHushAgentIsRenewedBeforeItsDeadline(t *testing.T) {
	hushE2EEnabled(t)
	const ttl = 6 * time.Second
	rig := newHushRig(t, "renew")
	rig.env = append(rig.env, "FIGARO_HUSH_TTL="+ttl.String())
	first := rig.start(t)
	t.Logf("agent up: %v", first)

	c := hush.NewWithSocket(filepath.Join(rig.hushRuntime, "agent.sock"))
	sealed, err := c.Encrypt(map[string]string{"v": "sk-never-sent"})
	if err != nil {
		t.Fatalf("seal a key with the rig's agent: %v", err)
	}
	providers := filepath.Join(rig.root, "config", "providers")
	if err := os.MkdirAll(providers, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(providers, "openrouter.toml"),
		[]byte(fmt.Sprintf("api_key = %q\nbase_url = \"http://127.0.0.1:9\"\n", sealed["v"])), 0o600); err != nil {
		t.Fatal(err)
	}
	// The keep-alive starts with the daemon's first use of hush: a decide
	// resolves the sealed key in the daemon, then fails to reach a closed port.
	out := rig.output(t, "-A", "decide", "--provider", "openrouter", "-m", "none", "--", "is the agent kept?")
	t.Logf("decide said: %s", strings.TrimSpace(out))

	seen := map[int]bool{}
	var failed int
	var worst, gap time.Duration
	var gapStart time.Time
	end := time.Now().Add(4 * ttl)
	for time.Now().Before(end) {
		if err := c.Ping(); err != nil {
			failed++
			if gapStart.IsZero() {
				gapStart = time.Now()
			}
		} else if !gapStart.IsZero() {
			if gap = time.Since(gapStart); gap > worst {
				worst = gap
			}
			gapStart = time.Time{}
		}
		for _, pid := range rig.agentPIDs(t) {
			seen[pid] = true
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !gapStart.IsZero() && time.Since(gapStart) > worst {
		worst = time.Since(gapStart)
	}
	t.Logf("over %v: %d agents, %d failed probes, longest gap %v", 4*ttl, len(seen), failed, worst)

	if len(seen) < 3 {
		t.Fatalf("saw %d agents over four lifetimes; the keep-alive did not replace it", len(seen))
	}
	if worst > 2*time.Second {
		t.Fatalf("the socket was empty for %v at once; a replacement should take well under that", worst)
	}
}
