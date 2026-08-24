package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/sdk"
)

// liveKeysStatus names the three distinct answers a live-key fetch can
// give. They used to be collapsed into "nil or not", and the collapse
// was a defect: "no aria bound" (the catalog is all that is knowable -
// offer it) and "an aria is bound but unreadable" (offering the catalog
// substitutes documentation for state) both looked like nil, so a shell
// attending a real aria was shown a hardcoded key list with every
// skills.* key silently missing, and only a human reading the
// candidates could tell.
type liveKeysStatus int

const (
	// liveKeysUnbound: no aria is bound to this shell: including the
	// no-daemon case, where nothing CAN be bound. The well-known
	// catalog is the honest and complete answer.
	liveKeysUnbound liveKeysStatus = iota

	// liveKeysOK: the bound aria's snapshot keys were fetched.
	liveKeysOK

	// liveKeysFetchFailed: an aria IS bound but its form could not be
	// read, a dead endpoint, a daemon from another revision refusing
	// the method, a timeout. The board's real keys exist and are
	// unknowable right now; a catalog offered here is a wrong answer
	// dressed as a right one.
	liveKeysFetchFailed
)

// completeFormKeys returns the union of well-known keys and
// live snapshot keys for the pid-bound aria. Used by both `set` and
// `unset`: no mode filtering, the runtime decides what's actionable.
// Templated keys like "system.environment.<name>" are expanded to
// one entry per allowlist member.
func completeFormKeys(c *cmdkit.CompleteContext) []string {
	live, status, err := softFetchLiveKeys()
	if status == liveKeysFetchFailed {
		fmt.Fprintf(stderrw, "figaro: completion: aria bound but form unreadable, offering no keys: %v\n", err)
		return nil
	}
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
	for _, d := range form.WellKnownKeys() {
		if strings.HasSuffix(d.Key, "<name>") {
			// Only the environment template has a known expander; other
			// <name>-shaped catalog entries are documentation-only and
			// rely on softFetchLiveKeys() to surface concrete instances.
			if d.Key == "system.environment.<name>" {
				prefix := strings.TrimSuffix(d.Key, "<name>")
				for _, name := range form.EnvironmentAllowlist {
					add(prefix + strings.ToLower(name))
				}
			}
			continue
		}
		add(d.Key)
	}
	for _, k := range live {
		add(k)
	}
	sort.Strings(out)
	return out
}

// softFetchLiveKeys best-effort fetches snapshot keys for the
// pid-bound aria. It never autostarts the daemon, prompts the user, or
// blocks long: but it does say which of the three facts it found
// (see liveKeysStatus): unbound, fetched, or bound-but-unreadable. The
// error accompanies liveKeysFetchFailed and names the failing step, so
// a mixed-version daemon ("method not found") is distinguishable from a
// dead aria socket at the one place anyone will ever look.
func softFetchLiveKeys() ([]string, liveKeysStatus, error) {
	ep := transport.UnixEndpoint(angelusSocketPath())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	acli, err := sdk.DialAngelus(ep)
	if err != nil {
		// No daemon, so no aria can be bound: the catalog is all that
		// is knowable, not a substitute for anything.
		return nil, liveKeysUnbound, nil
	}
	defer acli.Close()
	r, err := resolveBinding(ctx, acli, shellPID)
	if err != nil {
		return nil, liveKeysFetchFailed, fmt.Errorf("resolve pid binding: %w", err)
	}
	if !r.Found {
		return nil, liveKeysUnbound, nil
	}
	fep := transport.Endpoint{Scheme: r.Endpoint.Scheme, Address: r.Endpoint.Address}
	fcli, err := sdk.DialAria(fep, nil)
	if err != nil {
		return nil, liveKeysFetchFailed, fmt.Errorf("dial aria %s: %w", r.FigaroID, err)
	}
	defer fcli.Close()
	resp, err := fcli.Form(ctx)
	if err != nil {
		return nil, liveKeysFetchFailed, fmt.Errorf("read form of aria %s: %w", r.FigaroID, err)
	}
	return snapshotKeys(resp.Snapshot), liveKeysOK, nil
}

func snapshotKeys(snap form.Snapshot) []string {
	out := make([]string, 0, snap.Len())
	for k := range snap.All() {
		out = append(out, k)
	}
	return out
}
