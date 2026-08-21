package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/jack-work/figaro/internal/store"
)

// runDoctorTTL reports every node carrying system.ttl and when its lifetime is
// spent. It reads the sidecars directly -- no daemon, no store, no lock -- so
// it answers while the daemon is running, which is the only time anybody wants
// to ask. The daemon's own sweep reads the same numbers.
func runDoctorTTL(jsonOut bool) error {
	entries := store.ScanTTL(ariaRoot())
	rows := make([]store.TTLEntry, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, e)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].DeadlineMS < rows[j].DeadlineMS })

	if jsonOut {
		out := make([]map[string]any, 0, len(rows))
		for _, e := range rows {
			out = append(out, map[string]any{
				"id":            e.ID,
				"ttl":           e.TTL.String(),
				"created_at_ms": e.CreatedAtMS,
				"deadline_ms":   e.DeadlineMS,
				"expired":       e.Expired(time.Now().UnixMilli()),
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr,
			"no node states a lifetime (set one with: figaro set --id <id> system.ttl 30d)")
		return nil
	}
	now := time.Now()
	fmt.Printf("%-10s %-8s %-20s %-20s %s\n", "NODE", "TTL", "CREATED", "EXPIRES", "STATUS")
	due := 0
	for _, e := range rows {
		deadline := time.UnixMilli(e.DeadlineMS)
		status := "in " + deadline.Sub(now).Round(time.Minute).String()
		if e.Expired(now.UnixMilli()) {
			status = "DUE"
			due++
		}
		fmt.Printf("%-10s %-8s %-20s %-20s %s\n",
			e.ID, e.TTL.String(),
			time.UnixMilli(e.CreatedAtMS).Format("2006-01-02 15:04"),
			deadline.Format("2006-01-02 15:04"), status)
	}
	fmt.Fprintf(os.Stderr, "\n%d with a lifetime, %d due. The daemon's sweep takes what is due"+
		" once the aria is dormant and no shell is bound to it.\n", len(rows), due)
	return nil
}
