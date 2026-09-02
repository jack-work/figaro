package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
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
	resp := mustCallSet(loaded, ariaID, rpc.FormPatch{
		Set: map[string]json.RawMessage{"system.cwd": value},
	}, 0)
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
// completes a path.
func completeCdDirs(ctx *cmdkit.CompleteContext) []string {
	if len(ctx.Args) > 0 && ctx.Args[len(ctx.Args)-1] != ctx.Current {
		return nil
	}
	dir, prefix := filepath.Split(ctx.Current)
	search := dir
	if search == "" {
		search = "."
	}
	entries, err := os.ReadDir(search)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if prefix == "" && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, dir+e.Name()+string(filepath.Separator))
	}
	return out
}
