package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

// runDoctorTTL reports every node carrying system.ttl and when its lifetime is
// spent, as the daemon's own sweep sees it.
func runDoctorTTL(loaded *config.Loaded, jsonOut bool) error {
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	resp, err := acli.ListGlobal(context.Background())
	if err != nil {
		return err
	}
	var rows []rpc.FigaroInfoResponse
	for _, f := range resp.Figaros {
		if f.TTLMS > 0 {
			rows = append(rows, f)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ExpiresAt < rows[j].ExpiresAt })
	now := time.Now()
	ttlOf := func(f rpc.FigaroInfoResponse) time.Duration { return time.Duration(f.TTLMS) * time.Millisecond }

	if jsonOut {
		out := make([]map[string]any, 0, len(rows))
		for _, f := range rows {
			out = append(out, map[string]any{
				"id":            f.ID,
				"ttl":           ttlOf(f).String(),
				"created_at_ms": f.CreatedAt,
				"deadline_ms":   f.ExpiresAt,
				"expired":       f.ExpiresAt <= now.UnixMilli(),
			})
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	if len(rows) == 0 {
		fmt.Fprintln(stderrw,
			"no node states a lifetime (set one with: figaro set --id <id> system.ttl 30d)")
		return nil
	}
	fmt.Fprintf(stdout, "%-10s %-8s %-20s %-20s %s\n", "NODE", "TTL", "CREATED", "EXPIRES", "STATUS")
	due := 0
	for _, f := range rows {
		deadline := time.UnixMilli(f.ExpiresAt)
		status := "in " + deadline.Sub(now).Round(time.Minute).String()
		if f.ExpiresAt <= now.UnixMilli() {
			status = "DUE"
			due++
		}
		fmt.Fprintf(stdout, "%-10s %-8s %-20s %-20s %s\n",
			f.ID, ttlOf(f).String(),
			time.UnixMilli(f.CreatedAt).Format("2006-01-02 15:04"),
			deadline.Format("2006-01-02 15:04"), status)
	}
	fmt.Fprintf(stderrw, "\n%d with a lifetime, %d due. The daemon's sweep takes what is due"+
		" once the aria is dormant and no shell is bound to it.\n", len(rows), due)
	return nil
}
