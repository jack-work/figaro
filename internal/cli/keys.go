package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	figaro "github.com/jack-work/figaro"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/keyconf"
)

func keysPath(loaded *config.Loaded) string { return keyconf.Path(loaded.ConfigDir) }

func loadUserKeys(loaded *config.Loaded) {
	f, warnings, err := keyconf.Load(keysPath(loaded))
	if err != nil {
		fmt.Fprintf(stderrw, "figaro: %s\n", err)
		return
	}
	for _, w := range warnings {
		fmt.Fprintf(stderrw, "figaro: %s\n", w)
	}
	if f.Empty() {
		return
	}
	for _, d := range applyUserKeys(f) {
		fmt.Fprintf(stderrw, "figaro: keys.toml %s\n", d)
	}
}

const keysUsage = "usage: keys [layers | actions | init | bind <keys> <action> [<layer>] | unbind <keys> [<layer>] | reset [<keys> [<layer>]] | path]"

const keysExample = "skills/figaro/reference/keys.example.toml"

func keysInit(loaded *config.Loaded) error {
	path := keysPath(loaded)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s exists; `figaro keys reset` empties it, or move it aside", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	data, err := fs.ReadFile(figaro.Skills, keysExample)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(loaded.ConfigDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	fmt.Fprintln(stdout, path)
	return nil
}

func runKeys(loaded *config.Loaded, args []string) error {
	if len(args) == 0 {
		args = []string{"layers"}
	}
	switch args[0] {
	case "layers", "list":
		for _, line := range renderLayers(liveLayers) {
			fmt.Fprintln(stdout, line)
		}
		return nil
	case "actions":
		for _, line := range renderActions() {
			fmt.Fprintln(stdout, line)
		}
		return nil
	case "bind":
		if len(args) < 3 {
			return fmt.Errorf("usage: keys bind <keys> <action> [<layer>]")
		}
		return keysEdit(loaded, func(f *keyconf.File) error {
			f.Set(layerArg(args, 3), args[1], args[2])
			return nil
		})
	case "unbind":
		if len(args) < 2 {
			return fmt.Errorf("usage: keys unbind <keys> [<layer>]")
		}
		return keysEdit(loaded, func(f *keyconf.File) error {
			f.Set(layerArg(args, 2), args[1], "")
			return nil
		})
	case "reset":
		return keysEdit(loaded, func(f *keyconf.File) error {
			if len(args) < 2 {
				*f = keyconf.File{}
				return nil
			}
			if !f.Forget(layerArg(args, 2), args[1]) {
				return fmt.Errorf("keys.toml does not bind %s in %s", args[1], layerArg(args, 2))
			}
			return nil
		})
	case "init":
		return keysInit(loaded)
	case "path":
		fmt.Fprintln(stdout, keysPath(loaded))
		return nil
	}
	return fmt.Errorf("%s", keysUsage)
}

func layerArg(args []string, i int) string {
	if len(args) > i {
		return args[i]
	}
	return userLayerName
}

func keysEdit(loaded *config.Loaded, edit func(*keyconf.File) error) error {
	path := keysPath(loaded)
	f, _, err := keyconf.Load(path)
	if err != nil {
		return err
	}
	before := f
	if err := edit(&f); err != nil {
		return err
	}
	if diags := applyUserKeys(f); len(diags) > 0 {
		applyUserKeys(before)
		return fmt.Errorf("%s", diags[len(diags)-1])
	}
	if err := keyconf.Save(path, f); err != nil {
		return err
	}
	fmt.Fprintln(stdout, path)
	return nil
}

func runDoctorKeys(loaded *config.Loaded) error {
	path := keysPath(loaded)
	f, warnings, err := keyconf.Load(path)
	if err != nil {
		return err
	}
	if f.Empty() && len(warnings) == 0 {
		fmt.Fprintf(stdout, "%s: no keys.toml; the default map is in force\n", path)
		return nil
	}
	diags := applyUserKeys(f)
	for _, w := range warnings {
		fmt.Fprintln(stdout, "  "+w)
	}
	for _, line := range diagLines() {
		fmt.Fprintln(stdout, "  "+line)
	}
	fmt.Fprintf(stdout, "%s: %s refused\n", path, plural(len(diags), "binding"))
	if len(diags) > 0 || len(warnings) > 0 {
		return fmt.Errorf("keys.toml is not wholly in force")
	}
	return nil
}

func keysHelpLines() []string {
	return strings.Split(strings.TrimSpace(`
keys layers                       every layer, its modes and its bindings, highest first
keys init                         start a keys.toml from the shipped recipes
keys actions                      what a binding may name, and where it means something
keys bind <keys> <action> [layer] bind in a layer (default: user, above every other)
keys unbind <keys> [layer]        the key does nothing there, and nothing below shows through
keys reset [<keys> [layer]]       forget one of your bindings, or all of them
keys path                         where keys.toml lives`), "\n")
}
