package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

type tombstoneRow struct {
	ID          string `json:"id"`
	Reason      string `json:"reason"`
	CreatedAtMS int64  `json:"created_at_ms"`
	ReapedAtMS  int64  `json:"reaped_at_ms,omitempty"`
	PurgeAtMS   int64  `json:"purge_at_ms,omitempty"`
	Reaped      bool   `json:"reaped,omitempty"`
	Error       string `json:"error,omitempty"`
}

type tombstoneDoor interface {
	ListGlobal(ctx context.Context) (*rpc.ListResponse, error)
	Form(ctx context.Context, figaroID string) (*rpc.FormResponse, error)
	Kill(ctx context.Context, req rpc.KillRequest) (*rpc.KillResponse, error)
}

func runDoctorTombstones(loaded *config.Loaded, reap, dryRun, jsonOut bool) error {
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	rows, err := collectTombstones(context.Background(), acli, reap && !dryRun)
	if err != nil {
		return err
	}
	return printTombstones(stdout, stderrw, rows, reap, dryRun, jsonOut)
}

func collectTombstones(ctx context.Context, door tombstoneDoor, reap bool) ([]tombstoneRow, error) {
	resp, err := door.ListGlobal(ctx)
	if err != nil {
		return nil, err
	}
	var rows []tombstoneRow
	for _, f := range resp.Figaros {
		if strings.HasPrefix(f.ID, "@") || f.State == "anchor" || f.State == "form" {
			continue
		}
		formResp, err := door.Form(ctx, f.ID)
		if err != nil || formResp == nil || !form.RuntimeHas(formResp.Snapshot, store.TombstoneKey) {
			continue
		}
		reason := "tombstoned"
		if v := form.RuntimeLookup(formResp.Snapshot, store.TombstoneKey); v != nil && *v != "" {
			reason = *v
		}
		row := tombstoneRow{
			ID:          f.ID,
			Reason:      reason,
			CreatedAtMS: f.CreatedAt,
			ReapedAtMS:  f.ReapedAt,
		}
		if reap && f.ReapedAt == 0 {
			kresp, kerr := door.Kill(ctx, rpc.KillRequest{FigaroID: f.ID})
			if kerr != nil {
				row.Error = kerr.Error()
			} else {
				row.Reaped = true
				row.PurgeAtMS = kresp.PurgeAt
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAtMS != rows[j].CreatedAtMS {
			return rows[i].CreatedAtMS < rows[j].CreatedAtMS
		}
		return rows[i].ID < rows[j].ID
	})
	return rows, nil
}

func printTombstones(out, errOut io.Writer, rows []tombstoneRow, reap, dryRun, jsonOut bool) error {
	if rows == nil {
		rows = []tombstoneRow{}
	}
	reapedCount := 0
	for _, r := range rows {
		if r.Reaped {
			reapedCount++
		}
	}
	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"count":      len(rows),
			"reaped":     reapedCount,
			"tombstones": rows,
		})
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no tombstoned arias listed")
		return nil
	}
	fmt.Fprintf(out, "%-34s %-18s %-14s %s\n", "ARIA", "CREATED", "REASON", "STATUS")
	for _, r := range rows {
		created := "-"
		if r.CreatedAtMS > 0 {
			created = time.UnixMilli(r.CreatedAtMS).Format("2006-01-02 15:04")
		}
		status := "listed"
		switch {
		case r.Error != "":
			status = "error: " + r.Error
		case r.Reaped:
			status = "reaped"
		case reap && dryRun && r.ReapedAtMS == 0:
			status = "would reap"
		case r.ReapedAtMS > 0:
			status = "already reaped"
		}
		fmt.Fprintf(out, "%-34s %-18s %-14s %s\n", r.ID, created, r.Reason, status)
	}
	if !reap || dryRun {
		fmt.Fprintf(errOut, "\n%d tombstoned aria(s); reap with: figaro doctor tombstones --reap\n", len(rows))
	} else {
		fmt.Fprintf(errOut, "\nreaped %d of %d tombstoned aria(s)\n", reapedCount, len(rows))
	}
	return nil
}
