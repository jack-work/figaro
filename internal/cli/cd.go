package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
)

// runCd points an aria's system.cwd at path, resolved against the calling
// process's own working directory. No path is the home directory, as in a
// shell.
func runCd(loaded *config.Loaded, ariaID, path string) error {
	dir, err := resolveCdPath(path)
	if err != nil {
		return err
	}
	value, err := json.Marshal(dir)
	if err != nil {
		return err
	}
	resp := mustCallSet(loaded, ariaID,
		form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.cwd": value}, nil), 0)
	fmt.Fprintf(stderrw, "%s %s (figaro %s)%s\n",
		resp.verb("cd"), dir, resp.figaroID, resp.at())
	return nil
}

func resolveCdPath(path string) (string, error) {
	if path == "" || path == "~" {
		return os.UserHomeDir()
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, path[2:]), nil
	}
	return filepath.Abs(path)
}

// completeCdDirs offers directories under the partial token, the way a shell
// completes a path, and aria ids after --id. It could not expand ~ (it read a
// directory literally named "~") and went silent after `--id <aria>`.
func completeCdDirs(ctx *cmdkit.CompleteContext) []string {
	if ctx == nil {
		return nil
	}
	if n := len(ctx.Args); n > 0 && ctx.Args[n-1] == "--id" {
		return ariaCandidates(ctx)
	}
	for i, a := range ctx.Args {
		if a == "--id" {
			continue
		}
		if i > 0 && ctx.Args[i-1] == "--id" {
			continue
		}
		if !strings.HasPrefix(a, "-") {
			return nil // the directory is already named
		}
	}
	return pathCandidatesFor(ctx, ctx.Current, true)
}
