package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"sort"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/turns"
)

// runStatus resolves --id, then the positional arg, then the pid binding.
func runStatus(loaded *config.Loaded, idFlag string, args []string, more, jsonOut bool) {
	var nameArg string
	if len(args) > 0 {
		nameArg = args[0]
	}

	WithAngelusIfRunning(loaded, func(acli *sdk.Angelus) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		ariaID := idFlag
		if ariaID == "" {
			ariaID = nameArg
		}
		var resolveErr error
		if ariaID != "" {
			if id, err := doorID(ctx, acli, ariaID); err == nil {
				ariaID = id
			} else {
				resolveErr = err
			}
		}
		if ariaID == "" {
			r, err := resolveBinding(ctx, acli, shellPID)
			if err != nil {
				return fmt.Errorf("resolve: %w", err)
			}
			if !r.Found {
				die("no figaro bound to this shell (try: figaro status --id <id> or figaro status <id>)")
			}
			ariaID = r.FigaroID
		}

		// The global listing: a status target may be either species.
		resp, err := acli.ListGlobal(ctx)
		if err != nil {
			return fmt.Errorf("list: %w", err)
		}
		var f *rpc.FigaroInfoResponse
		for i := range resp.Figaros {
			if resp.Figaros[i].ID == ariaID {
				f = &resp.Figaros[i]
				break
			}
		}
		if f == nil {
			if resolveErr != nil && !strings.Contains(resolveErr.Error(), "not found") {
				die("%s", resolveErr)
			}
			die("no aria or form %q (try: figaro ls -g)", ariaID)
		}

		if isFormRow(f) {
			snap, version := softFormSnapshot(loaded, f.ID)
			target := roleTargetOf(f, snap)
			targetState := ""
			if target != "" {
				targetState = "missing"
				for i := range resp.Figaros {
					if resp.Figaros[i].ID == target {
						targetState = resp.Figaros[i].State
						if targetState == "" {
							targetState = "known"
						}
						break
					}
				}
			}
			if jsonOut {
				st := formStatus(f, snap, version)
				if targetState != "" {
					st["target_state"] = targetState
				}
				return json.NewEncoder(stdout).Encode(st)
			}
			printFormStatusPanel(os.Stdout, f, snap, version, targetState, more)
			return nil
		}

		if jsonOut {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(f)
		}
		// The tree records a fork point as an LT; the user's coordinate is
		// a turn.
		var forkTurn uint64
		if len(f.Vector) > 1 && f.Parent != "" && f.BranchedLT > 1 {
			if msgs, merr := ariaMessages(ctx, acli, f.Parent); merr == nil {
				if t, ok := turns.At(msgs, f.BranchedLT); ok {
					forkTurn = t
				}
			}
		}
		printStatusPanel(os.Stdout, f, more, forkTurn)
		return nil
	})
}

// The sigil is the fallback for a row that predates kinds in the listing.
func isFormRow(f *rpc.FigaroInfoResponse) bool {
	return f.Kind == "form" || f.Kind == "outfit" || strings.HasPrefix(f.ID, "@")
}

// softFormSnapshot is best effort: an unanswering endpoint still reports what the
// listing knows.
func softFormSnapshot(loaded *config.Loaded, id string) (form.Snapshot, uint64) {
	var snap form.Snapshot
	var version uint64
	WithSessionFor(loaded, id, func(s *Session) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := s.Figaro.Form(ctx)
		if err != nil {
			return nil
		}
		snap, version = resp.Snapshot, resp.Version
		return nil
	})
	return snap, version
}

func formStatus(f *rpc.FigaroInfoResponse, snap form.Snapshot, version uint64) map[string]any {
	out := map[string]any{
		"id":      f.ID,
		"species": speciesOf(f),
		"version": version,
		"keys":    snap.Len(),
	}
	if f.Name != "" {
		out["name"] = f.Name
	}
	if t := roleTargetOf(f, snap); t != "" {
		out["target_aria"] = t
	}
	if f.Parent != "" {
		out["parent"] = f.Parent
	}
	if f.LastActive != 0 {
		out["last_active"] = f.LastActive
	}
	if len(f.BoundPIDs) > 0 {
		out["bound_pids"] = f.BoundPIDs
	}
	return out
}

// A role is a duck type: an unbound form carrying target-aria.
func speciesOf(f *rpc.FigaroInfoResponse) string {
	switch {
	case !isFormRow(f):
		return "figaro"
	case f.TargetAria != "":
		return "role"
	case f.Kind == "outfit":
		return "form (legacy outfit stump)"
	default:
		return "form"
	}
}

// The listing can lag a cast by one refresh; the board is the truth.
func roleTargetOf(f *rpc.FigaroInfoResponse, snap form.Snapshot) string {
	if f.TargetAria != "" {
		return f.TargetAria
	}
	if raw, ok := snap.Get("target-aria"); ok {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	return ""
}

func printFormStatusPanel(out *os.File, f *rpc.FigaroInfoResponse, snap form.Snapshot, version uint64, targetState string, more bool) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	row := func(k, v string) { fmt.Fprintf(w, "  %s:\t%s\n", k, v) }
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}

	species := speciesOf(f)
	fmt.Fprintf(w, "%s\t%s\n", species, f.ID)
	row("name", dash(f.Name))
	if target := roleTargetOf(f, snap); target != "" {
		if targetState != "" {
			target += " (" + targetState + ")"
		}
		row("target-aria", target)
	} else if species == "form" {
		row("target-aria", "- (not cast: this form is not a role)")
	}
	row("version", fmt.Sprintf("%d", version))
	row("keys", fmt.Sprintf("%d", snap.Len()))

	if f.LastActive != 0 {
		ts := time.UnixMilli(f.LastActive)
		row("last-active", fmt.Sprintf("%s (%s ago)",
			ts.Format("2006-01-02 15:04:05"), truncateDuration(time.Since(ts))))
	} else {
		row("last-active", "-")
	}

	pids := "-"
	if len(f.BoundPIDs) > 0 {
		strs := make([]string, len(f.BoundPIDs))
		for i, p := range f.BoundPIDs {
			strs[i] = fmt.Sprintf("%d", p)
		}
		pids = strings.Join(strs, ",")
	}
	row("attended-by", pids)

	if more {
		row("parent", dash(f.Parent))
		names := make([]string, 0, snap.Len())
		for k := range snap.All() {
			if !strings.HasPrefix(k, "system.") {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		row("keys held", dash(strings.Join(names, ", ")))
		row("bind", "figaro bind "+f.ID)
	}

	w.Flush()
}

// Empty or zero fields print "-" so unknown is tellable from zero.
func printStatusPanel(out *os.File, f *rpc.FigaroInfoResponse, more bool, forkTurn uint64) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	row := func(k, v string) { fmt.Fprintf(w, "  %s:\t%s\n", k, v) }
	rowf := func(k, format string, args ...any) { row(k, fmt.Sprintf(format, args...)) }
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}

	fmt.Fprintf(w, "figaro\t%s\n", f.ID)
	row("state", dash(f.State))
	row("mantra", dash(f.Mantra))
	row("provider", dash(f.Provider))
	row("model", dash(f.Model))
	rowf("messages", "%d", f.MessageCount)

	row("context", formatContextUsage(f.ContextTokens, f.ContextLimit, f.ContextExact))

	usage := "-"
	if f.TokensIn > 0 || f.TokensOut > 0 {
		usage = fmt.Sprintf("%d in / %d out", f.TokensIn, f.TokensOut)
	}
	row("tokens", usage)
	row("cost", formatSessionTokenCost(f.TokensIn, f.TokensOut))

	cache := "-"
	if f.CacheReadTokens > 0 || f.CacheWriteTokens > 0 {
		cache = fmt.Sprintf("%d read / %d write", f.CacheReadTokens, f.CacheWriteTokens)
	}
	row("cache", cache)

	if f.LastActive != 0 {
		ts := time.UnixMilli(f.LastActive)
		row("last-active", fmt.Sprintf("%s (%s ago)",
			ts.Format("2006-01-02 15:04:05"),
			truncateDuration(time.Since(ts))))
	} else {
		row("last-active", "-")
	}

	pids := "-"
	if len(f.BoundPIDs) > 0 {
		strs := make([]string, len(f.BoundPIDs))
		for i, p := range f.BoundPIDs {
			strs[i] = fmt.Sprintf("%d", p)
		}
		pids = strings.Join(strs, ",")
	}
	row("bound-pids", pids)

	if more {
		row("cwd", dash(f.Cwd))
		outfit := dash(f.OutfitName)
		if f.OutfitVer != "" {
			outfit += " (" + f.OutfitVer + ")"
		}
		row("outfit", outfit)
		if f.CreatedAt != 0 {
			row("created", time.UnixMilli(f.CreatedAt).Format("2006-01-02 15:04:05"))
		}
		if len(f.Vector) > 1 && f.Parent != "" && f.BranchedLT > 1 {
			if forkTurn > 0 {
				rowf("forked-from", "%s:%d", f.Parent, forkTurn)
			} else {
				rowf("forked-from", "%s", f.Parent)
			}
		}
	}

	w.Flush()
}

func truncateDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(time.Second).String()
	case d < time.Hour:
		return d.Round(time.Minute).String()
	default:
		return d.Round(time.Minute).String()
	}
}
