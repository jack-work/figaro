package cli

import (
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/internal/store"
)

func runDoctorNormalizeOffline(from string, revert, jsonOut bool) error {
	b, err := store.NewXwalBackend(from, 0)
	if err != nil {
		return fmt.Errorf("doctor normalize --from %s: %w", from, err)
	}
	rep := b.RuntimeNamesAtOpen()
	if revert {
		if rep, err = b.RevertRuntimeNames(); err != nil {
			b.Close()
			return fmt.Errorf("doctor normalize --revert: %w", err)
		}
	}
	if err := b.Close(); err != nil {
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	if !revert && rep.Forms == 0 {
		fmt.Fprintln(stdout, "the store already keeps daemon-written keys under runtime.*")
		return nil
	}
	verb := "moved to runtime.*"
	if revert {
		verb = "moved back to system.*; open it with a figaro from before runtime.* or this one migrates it again"
	}
	fmt.Fprintf(stdout, "%d of %d forms renamed in %d ms: %s\n", rep.Renamed, rep.Forms, rep.Millis, verb)
	return nil
}
