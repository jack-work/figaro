package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"sort"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/turns"
)

// runStatus prints a focused single-aria view of the target figaro.
// Target resolution: --id flag > positional arg > pid-bound. Reads the
// same FigaroInfoResponse the list view uses; for dormant arias the
// angelus backfills from the meta derivation. With no live data and no
// derivation file, fields will read "-". more surfaces the derived/extra
// detail; jsonOut emits the whole response as JSON.
func runStatus(loaded *config.Loaded, idFlag string, args []string, more, jsonOut bool) {
	var nameArg string
	if len(args) > 0 {
		nameArg = args[0]
	}

	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		ariaID := idFlag
		if ariaID == "" {
			ariaID = nameArg
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

		// The GLOBAL listing, because a status target may be either species.
		// `fig ls` is figaros only, which is why status on an attended FORM
		// used to answer "no aria @58a57f36" while the form sat right there.
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
			die("no aria or form %q (try: figaro ls -g)", ariaID)
		}

		// An unbound form is not a conversation, and a panel of turn counts
		// and token usage would be a panel of dashes. It gets its own.
		if isFormRow(f) {
			snap, version := softFormSnapshot(loaded, f.ID)
			// A role's most useful fact after WHERE it points is what it
			// points AT: an aria that is live, asleep, or no longer there.
			// The listing is already in hand, so this costs nothing.
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
				return json.NewEncoder(os.Stdout).Encode(st)
			}
			printFormStatusPanel(os.Stdout, f, snap, version, targetState, more)
			return nil
		}

		if jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(f)
		}
		// The forest records a fork point as an LT, but the user's coordinate is
		// a turn: resolve it so the panel can name what `fork <id>:N` takes.
		//
		// Resolve against the PARENT, not this aria: BranchedLT is the first LT
		// this branch owns, and a fresh branch has not written it yet, so its own
		// log cannot name the turn. The turn that was replaced lives in the
		// parent, which is exactly what "forked-from parent:turn" means. Best
		// effort, an unreadable parent just omits the turn rather than failing.
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

// isFormRow reports whether a listing row is an unbound form. The kind comes
// from the figwal marker; the sigil is the fallback for a row that predates
// kinds being carried in the listing.
func isFormRow(f *rpc.FigaroInfoResponse) bool {
	return f.Kind == "form" || f.Kind == "outfit" || strings.HasPrefix(f.ID, "@")
}

// softFormSnapshot reads a form's state and version. Best effort: status is a
// read, and a form whose endpoint will not answer should still report what the
// listing knows rather than failing the whole command.
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

// formStatus is the JSON shape for a form or a role: the listing row, plus
// what only the board itself can say.
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

// speciesOf names what a row IS, which is the first thing status should say.
// A role is a duck type: an unbound form carrying target-aria. Nothing
// converts, so this is a reading of the state, not a stored kind.
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

// roleTargetOf reads target-aria from the listing, falling back to the board:
// the listing is derived and can lag a cast by one refresh, and the board is
// the truth.
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

// printFormStatusPanel is status for an unbound form, and for a role.
//
// It answers the question status is FOR, which for a form is not "how many
// turns" but "what is this, what does it hold, and who is it pointed at".
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
		// Saying it plainly is the point: the difference between a form and a
		// role is one key, and a reader should not have to know that.
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

// printStatusPanel renders a key/value view of a single figaro. Empty
// or zero fields collapse to "-" so the user can tell what's known
// vs. unknown rather than guessing whether "0" is real or stale.
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
	// STATE IS NOT AN OUTCOME. "idle" says the inbox is empty; it says
	// nothing about whether the last thing the user asked for worked. An
	// aria whose last turn died on a provider error reported "idle" and
	// nothing else, and that gap is precisely how a failed turn gets
	// reported as a hang. When the last turn failed, say so on the line
	// under state, where it cannot be missed.
	if reason := lastTurnRow(f); reason != "" {
		row("last-turn", reason)
	}
	if f.UnansweredInputs > 0 {
		// Work taken and nothing produced. Distinct from a queue backlog:
		// these are already committed to the log.
		row("unanswered", fmt.Sprintf("%d prompt(s) with no reply", f.UnansweredInputs))
	}
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

	// Derived / extra detail (formerly the `derive` command's territory).
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

// truncateDuration rounds to the largest unit that fits cleanly.
// Avoids "3h4m5.123456789s"; gives "3h4m" / "12m" / "45s".
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

// lastTurnRow renders how the last turn ended, or "" when there is nothing
// worth saying. A turn that simply completed needs no row: state already
// covers the healthy case, and a status panel that reports every success
// teaches people to skim past the one line that mattered.
func lastTurnRow(f *rpc.FigaroInfoResponse) string {
	reason := strings.TrimSpace(f.LastTurnReason)
	if reason == "" {
		return ""
	}
	low := strings.ToLower(reason)
	failed := strings.HasPrefix(low, "error:")
	if !failed && !strings.Contains(low, "interrupt") {
		return ""
	}
	if f.LastTurnAt != 0 {
		return fmt.Sprintf("%s (%s ago)", reason, truncateDuration(time.Since(time.UnixMilli(f.LastTurnAt))))
	}
	return reason
}
