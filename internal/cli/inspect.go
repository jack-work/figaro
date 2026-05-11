package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jack-work/figaro/internal/config"
)

// runRehydrate re-runs the credo on the bound figaro.
func runRehydrate(loaded *config.Loaded) {
	dryRun := false
	for _, arg := range os.Args[2:] {
		switch arg {
		case "--dry-run", "-n":
			dryRun = true
		default:
			die("unknown flag: %s", arg)
		}
	}
	runRehydrateWithFlag(loaded, dryRun)
}

func runRehydrateWithFlag(loaded *config.Loaded, dryRun bool) {
	WithSession(loaded, func(s *Session) error {
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
