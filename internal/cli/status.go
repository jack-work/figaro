package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/rpc"
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
			r, err := acli.Resolve(ctx, os.Getppid())
			if err != nil {
				return fmt.Errorf("resolve: %w", err)
			}
			if !r.Found {
				die("no figaro bound to this shell (try: figaro status --id <id> or figaro status <id>)")
			}
			ariaID = r.FigaroID
		}

		resp, err := acli.List(ctx)
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
			die("no aria %q (try: figaro list)", ariaID)
		}

		if jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(f)
		}
		printStatusPanel(os.Stdout, f, more)
		return nil
	})
}

// printStatusPanel renders a key/value view of a single figaro. Empty
// or zero fields collapse to "-" so the user can tell what's known
// vs. unknown rather than guessing whether "0" is real or stale.
func printStatusPanel(out *os.File, f *rpc.FigaroInfoResponse, more bool) {
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
	row("provider", dash(f.Provider))
	row("model", dash(f.Model))
	rowf("messages", "%d", f.MessageCount)

	ctxStr := "-"
	if f.ContextTokens > 0 {
		ctxStr = fmt.Sprintf("%dk", f.ContextTokens/1000)
		if !f.ContextExact {
			ctxStr = "~" + ctxStr
		}
	}
	row("context", ctxStr)

	usage := "-"
	if f.TokensIn > 0 || f.TokensOut > 0 {
		usage = fmt.Sprintf("%d in / %d out", f.TokensIn, f.TokensOut)
	}
	row("tokens", usage)

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
		row("mantra", dash(f.Mantra))
		row("cwd", dash(f.Cwd))
		loadout := dash(f.LoadoutName)
		if f.LoadoutVer != "" {
			loadout += " (" + f.LoadoutVer + ")"
		}
		row("loadout", loadout)
		if f.CreatedAt != 0 {
			row("created", time.UnixMilli(f.CreatedAt).Format("2006-01-02 15:04:05"))
		}
		if f.Parent != "" {
			rowf("forked-from", "%s @ LT %d", f.Parent, f.BranchedLT-1)
		}
		if f.Frozen {
			row("frozen", "yes")
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
