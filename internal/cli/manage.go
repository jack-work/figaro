package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/internal/config"
)

// runList prints the registry of all figaros (live and dormant).
func runList(loaded *config.Loaded) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	resp, err := acli.List(ctx)
	if err != nil {
		die("list: %s", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "\tID\tSTATE\tMODEL\tMSGS\tCONTEXT\tCACHE\tPIDS\n")
	for _, f := range resp.Figaros {
		pids := make([]string, len(f.BoundPIDs))
		for i, p := range f.BoundPIDs {
			pids[i] = fmt.Sprintf("%d", p)
		}
		pidStr := strings.Join(pids, ",")
		if pidStr == "" {
			pidStr = "-"
		}
		ctxStr := fmt.Sprintf("%dk", f.ContextTokens/1000)
		if !f.ContextExact {
			ctxStr = "~" + ctxStr
		}
		cacheStr := "-"
		if f.CacheReadTokens > 0 || f.CacheWriteTokens > 0 {
			cacheStr = fmt.Sprintf("%dk/%dk", f.CacheReadTokens/1000, f.CacheWriteTokens/1000)
		}
		current := ""
		if slices.Contains(f.BoundPIDs, os.Getppid()) {
			current = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			current, f.ID, f.State, f.Model, f.MessageCount, ctxStr, cacheStr, pidStr)
	}
	w.Flush()
}

func runKill(loaded *config.Loaded) {
	if len(os.Args) < 3 {
		die("usage: figaro kill <id>")
	}
	figaroID := os.Args[2]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	if err := acli.Kill(ctx, figaroID); err != nil {
		die("kill: %s", err)
	}
	fmt.Fprintf(os.Stderr, "killed %s\n", figaroID)
}

func runAttend(loaded *config.Loaded) {
	if len(os.Args) < 3 {
		die("usage: figaro attend <id>")
	}
	figaroID := os.Args[2]

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := os.Getppid()
	acli.Unbind(ctx, ppid)

	if err := acli.Bind(ctx, ppid, figaroID); err != nil {
		die("attend: %s", err)
	}
	fmt.Fprintf(os.Stderr, "attending %s\n", figaroID)
}

// runDetach unbinds this shell's PPID from whatever figaro it is
// currently attached to. The figaro stays alive; the next `q` call
// from this shell will create a fresh figaro (or prompt for attend).
func runDetach(loaded *config.Loaded) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	ppid := os.Getppid()

	resp, err := acli.Resolve(ctx, ppid)
	if err != nil {
		die("resolve: %s", err)
	}
	if !resp.Found {
		fmt.Fprintln(os.Stderr, "no figaro bound to this shell")
		return
	}

	if err := acli.Unbind(ctx, ppid); err != nil {
		die("unbind: %s", err)
	}
	fmt.Fprintf(os.Stderr, "detached from %s\n", resp.FigaroID)
}
