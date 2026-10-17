package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/sdk"
)

// liveKeysStatus distinguishes "no aria bound" (offer the catalog) from "bound
// but unreadable" (the catalog would substitute documentation for state).
type liveKeysStatus int

const (
	// liveKeysUnbound includes the no-daemon case.
	liveKeysUnbound liveKeysStatus = iota

	liveKeysOK

	// liveKeysFetchFailed: an aria is bound but its form could not be read.
	liveKeysFetchFailed
)

// completeFormKeys is the union of well-known keys and the bound aria's live
// keys; a skill is offered as itself as well as by its leaves.
func completeFormKeys(c *cmdkit.CompleteContext) []string {
	snap, status, err := softFetchLiveSnapshot()
	if status == liveKeysFetchFailed {
		fmt.Fprintf(stderrw, "figaro: completion: aria bound but form unreadable, offering no keys: %v\n", err)
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(k, desc string) {
		if k == "" {
			return
		}
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		out = append(out, described(c, k, desc))
	}
	for _, d := range form.WellKnownKeys() {
		if strings.HasSuffix(d.Key, "<name>") {
			// Only the environment template has an expander.
			if d.Key == "system.environment.<name>" {
				prefix := strings.TrimSuffix(d.Key, "<name>")
				for _, name := range form.EnvironmentAllowlist {
					add(prefix+strings.ToLower(name), d.Short)
				}
			}
			continue
		}
		add(d.Key, d.Short)
	}
	if status == liveKeysOK {
		for k, v := range snap.All() {
			rest, ok := strings.CutPrefix(k, "skills.")
			if !ok {
				continue
			}
			name, leaf, ok := strings.Cut(rest, ".")
			if !ok || leaf != "frontmatter" {
				continue
			}
			add("skills."+name, "skill · "+skillDescription(valueSummaryRaw(v)))
		}
		for k, v := range snap.All() {
			add(k, valueSummary(v))
		}
	}
	sort.Strings(out)
	return out
}

func valueSummaryRaw(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func softFetchLiveKeys() ([]string, liveKeysStatus, error) {
	snap, status, err := softFetchLiveSnapshot()
	if status != liveKeysOK {
		return nil, status, err
	}
	return snapshotKeys(snap), status, nil
}

// softFetchLiveSnapshot never autostarts the daemon, prompts, or blocks long;
// the error names the failing step.
func softFetchLiveSnapshot() (form.Snapshot, liveKeysStatus, error) {
	ep := doorEndpoint()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	acli, err := sdk.DialAngelus(ep)
	if err != nil {
		return form.Snapshot{}, liveKeysUnbound, nil
	}
	defer acli.Close()
	var r *rpc.ResolveResponse
	if completionSubject != "" {
		r, err = resolveEnvAria(ctx, acli, completionSubject)
	} else {
		r, err = resolveBinding(ctx, acli, shellPID)
	}
	if err != nil {
		return form.Snapshot{}, liveKeysFetchFailed, fmt.Errorf("resolve pid binding: %w", err)
	}
	if !r.Found {
		return form.Snapshot{}, liveKeysUnbound, nil
	}
	fcli, err := sdk.DialAria(ariaAddr(r.FigaroID), nil)
	if err != nil {
		return form.Snapshot{}, liveKeysFetchFailed, fmt.Errorf("dial aria %s: %w", r.FigaroID, err)
	}
	defer fcli.Close()
	resp, err := fcli.Form(ctx)
	if err != nil {
		return form.Snapshot{}, liveKeysFetchFailed, fmt.Errorf("read form of aria %s: %w", r.FigaroID, err)
	}
	return resp.Snapshot, liveKeysOK, nil
}

func snapshotKeys(snap form.Snapshot) []string {
	out := make([]string, 0, snap.Len())
	for k := range snap.All() {
		out = append(out, k)
	}
	return out
}
