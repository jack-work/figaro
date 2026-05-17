package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jack-work/figaro/internal/config"
)

// runRehydrateWithFlag re-runs the credo on the target figaro.
func runRehydrateWithFlag(loaded *config.Loaded, ariaID string, dryRun bool) {
	WithSessionFor(loaded, ariaID, func(s *Session) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		rresp, err := s.Figaro.ReloadConfig(ctx, dryRun)
		if err != nil {
			die("rehydrate: %s", err)
		}

		if len(rresp.SetKeys) == 0 && len(rresp.RemoveKeys) == 0 {
			fmt.Fprintln(os.Stderr, "rehydrate: no changes")
			return nil
		}
		verb := "applied"
		if dryRun {
			verb = "would apply"
		}
		fmt.Fprintf(os.Stderr, "rehydrate: %s set=%v remove=%v\n", verb, rresp.SetKeys, rresp.RemoveKeys)
		return nil
	})
}
