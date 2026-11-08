package cli

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/store"
)

func runDoctorSnapshot(to, from, arias, maxMB string, jsonOut bool) error {
	if from == "" {
		from = boot.AriaRoot()
	}
	opt := store.SliceOptions{Arias: 50, MaxBytes: 500 << 20}
	if arias != "" {
		n, err := strconv.Atoi(arias)
		if err != nil || n < 0 {
			return fmt.Errorf("doctor snapshot: --arias takes a count, 0 for all, not %q", arias)
		}
		opt.Arias = n
	}
	if maxMB != "" {
		n, err := strconv.ParseInt(maxMB, 10, 64)
		if err != nil || n < 0 {
			return fmt.Errorf("doctor snapshot: --max-mb takes megabytes, 0 for no cap, not %q", maxMB)
		}
		opt.MaxBytes = n << 20
	}
	rep, err := store.CopySlice(from, to, opt)
	if err != nil {
		return fmt.Errorf("doctor snapshot: %w", err)
	}
	if jsonOut {
		return json.NewEncoder(stdout).Encode(map[string]any{
			"from": from, "to": to, "arias": rep.Arias, "candidates": rep.Candidates,
			"skipped": rep.Skipped, "nodes": rep.Nodes, "bytes": rep.Bytes, "millis": rep.Elapsed.Milliseconds(),
		})
	}
	fmt.Fprintf(stdout, "copied %d of %d arias (%d nodes, %.0f MB) from %s to %s in %s",
		rep.Arias, rep.Candidates, rep.Nodes, float64(rep.Bytes)/(1<<20), from, to, rep.Elapsed.Round(10e6))
	if rep.Skipped > 0 {
		fmt.Fprintf(stdout, "; %d passed over for the size cap", rep.Skipped)
	}
	fmt.Fprintln(stdout)
	return nil
}
