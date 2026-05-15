package cli

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/transport"
)

// completeChalkboardKeys returns the union of well-known keys and
// live snapshot keys for the pid-bound aria. Used by both `set` and
// `unset` — no mode filtering, the runtime decides what's actionable.
// Templated keys like "system.environment.<name>" are expanded to
// one entry per allowlist member.
func completeChalkboardKeys(_ *cmdkit.CompleteContext) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(k string) {
		if k == "" {
			return
		}
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	for _, d := range chalkboard.WellKnownKeys() {
		if strings.HasSuffix(d.Key, "<name>") {
			prefix := strings.TrimSuffix(d.Key, "<name>")
			for _, name := range chalkboard.EnvironmentAllowlist {
				add(prefix + strings.ToLower(name))
			}
			continue
		}
		add(d.Key)
	}
	for _, k := range softFetchLiveKeys() {
		add(k)
	}
	sort.Strings(out)
	return out
}

// softFetchLiveKeys best-effort fetches snapshot keys for the
// pid-bound aria. Returns nil on any failure: completion must never
// autostart the daemon, prompt the user, or block long.
func softFetchLiveKeys() []string {
	ep := transport.UnixEndpoint(angelusSocketPath())
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	acli, err := angelus.DialClient(ep)
	if err != nil {
		return nil
	}
	defer acli.Close()
	r, err := acli.Resolve(ctx, os.Getppid())
	if err != nil || !r.Found {
		return nil
	}
	fep := transport.Endpoint{Scheme: r.Endpoint.Scheme, Address: r.Endpoint.Address}
	fcli, err := figaro.DialClient(fep, nil)
	if err != nil {
		return nil
	}
	defer fcli.Close()
	resp, err := fcli.Chalkboard(ctx)
	if err != nil {
		return nil
	}
	return snapshotKeys(resp.Snapshot)
}

func snapshotKeys(snap map[string]json.RawMessage) []string {
	out := make([]string, 0, len(snap))
	for k := range snap {
		out = append(out, k)
	}
	return out
}
