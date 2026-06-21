package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
)

// runList prints the registry of all figaros (live and dormant).
func runList(loaded *config.Loaded) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		resp, err := acli.List(ctx)
		if err != nil {
			die("list: %s", err)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintf(w, "\tID\tSTATE\tAGE\tMODEL\tMSGS\tCONTEXT\tCACHE\tPIDS\tCWD\tMANTRA\n")
		for _, f := range resp.Figaros {
			pids := make([]string, len(f.BoundPIDs))
			for i, p := range f.BoundPIDs {
				pids[i] = fmt.Sprintf("%d", p)
			}
			pidStr := strings.Join(pids, ",")
			if pidStr == "" {
				pidStr = "-"
			}
			ctxStr := "-"
			if f.ContextTokens > 0 {
				ctxStr = fmt.Sprintf("%dk", f.ContextTokens/1000)
				if !f.ContextExact {
					ctxStr = "~" + ctxStr
				}
			}
			cacheStr := "-"
			if f.CacheReadTokens > 0 || f.CacheWriteTokens > 0 {
				cacheStr = fmt.Sprintf("%dk/%dk", f.CacheReadTokens/1000, f.CacheWriteTokens/1000)
			}
			model := f.Model
			if model == "" {
				model = "-"
			}
			current := ""
			if slices.Contains(f.BoundPIDs, os.Getppid()) {
				current = "*"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
				current, f.ID, f.State, relAge(f.LastActive), model, f.MessageCount,
				ctxStr, cacheStr, pidStr, shortCwd(f.Cwd), dash(f.Mantra))
		}
		w.Flush()
		return nil
	})
}

// dash returns "-" for an empty string.
func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// relAge renders a unix-millis timestamp as a compact age (e.g. "4m", "2h",
// "3d"); "-" when unknown.
func relAge(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// shortCwd shortens a path for the table: $HOME → ~, then keep the tail if
// it's long. "-" when empty.
func shortCwd(p string) string {
	if p == "" {
		return "-"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		p = "~" + strings.TrimPrefix(p, home)
	}
	const max = 28
	if len(p) > max {
		p = "…" + p[len(p)-max+1:]
	}
	return p
}

func runKill(loaded *config.Loaded, idFlag string, args []string) {
	ariaID := idFlag
	if ariaID == "" && len(args) > 0 {
		ariaID = args[0]
	}
	if ariaID == "" {
		die("usage: figaro kill [--id <id> | <id>]")
	}
	runKillByID(loaded, ariaID)
}

func runKillByID(loaded *config.Loaded, figaroID string) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := acli.Kill(ctx, figaroID); err != nil {
			die("kill: %s", err)
		}
		fmt.Fprintf(os.Stderr, "killed %s\n", figaroID)
		return nil
	})
}

func runAttendByID(loaded *config.Loaded, figaroID string) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ppid := os.Getppid()
		acli.Unbind(ctx, ppid)
		if err := acli.Bind(ctx, ppid, figaroID); err != nil {
			die("attend: %s", err)
		}
		fmt.Fprintf(os.Stderr, "attending %s\n", figaroID)
		return nil
	})
}

// runDetach unbinds this shell's PPID from its figaro.
func runDetach(loaded *config.Loaded) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ppid := os.Getppid()
		resp, err := acli.Resolve(ctx, ppid)
		if err != nil {
			die("resolve: %s", err)
		}
		if !resp.Found {
			fmt.Fprintln(os.Stderr, "no figaro bound to this shell")
			return nil
		}
		if err := acli.Unbind(ctx, ppid); err != nil {
			die("unbind: %s", err)
		}
		fmt.Fprintf(os.Stderr, "detached from %s\n", resp.FigaroID)
		return nil
	})
}
