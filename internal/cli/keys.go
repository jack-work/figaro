package cli

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/keyconf"
)

// keysPath is the file this process reads and writes. Client-owned: the
// daemon never sees it, so there is no RPC and no refresh in the way.
func keysPath(loaded *config.Loaded) string { return keyconf.Path(loaded.ConfigDir) }

// loadUserKeys applies keys.toml over the defaults, once, at startup. It is
// deliberately NOT called from init(): the default table is what every keymap
// test measures, and a developer's own config must not rewrite it.
func loadUserKeys(loaded *config.Loaded) {
	rows, warnings, err := keyconf.Load(keysPath(loaded))
	if err != nil {
		fmt.Fprintf(stderrw, "figaro: %s\n", err)
		return
	}
	for _, w := range warnings {
		fmt.Fprintf(stderrw, "figaro: %s\n", w)
	}
	if len(rows) == 0 {
		return
	}
	for _, d := range applyUserKeys(rows) {
		fmt.Fprintf(stderrw, "figaro: keys.toml %s\n", d)
	}
}

func runKeys(loaded *config.Loaded, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		for _, line := range renderKeyListing(listKeys(liveRows)) {
			fmt.Fprintln(stdout, line)
		}
		return nil
	case "actions":
		for _, a := range keyActions {
			level := "pager"
			if a.input != nil {
				level = "input"
			}
			fmt.Fprintf(stdout, "  %-24s %s\n", a.name, level)
		}
		return nil
	case "bind":
		if len(args) < 3 {
			return fmt.Errorf("usage: keys bind <chord> <action> [<modes>]")
		}
		return keysWrite(loaded, keyconf.Row{Chord: args[1], Action: args[2], Mode: modeArg(args, 3)}, false)
	case "unbind":
		if len(args) < 2 {
			return fmt.Errorf("usage: keys unbind <chord> [<modes>]")
		}
		return keysWrite(loaded, keyconf.Row{Chord: args[1], Action: "none", Mode: modeArg(args, 2)}, false)
	case "reset":
		if len(args) < 2 {
			return keysWrite(loaded, keyconf.Row{}, true)
		}
		return keysWrite(loaded, keyconf.Row{Chord: args[1], Mode: modeArg(args, 2)}, true)
	case "path":
		fmt.Fprintln(stdout, keysPath(loaded))
		return nil
	}
	return fmt.Errorf("usage: keys <list | actions | bind <chord> <action> [<modes>] | unbind <chord> [<modes>] | reset [<chord>] | path>")
}

func modeArg(args []string, i int) string {
	if len(args) > i {
		return args[i]
	}
	return ""
}

// keysWrite edits keys.toml and reports what the edit would do, refusing
// before it writes rather than after.
func keysWrite(loaded *config.Loaded, row keyconf.Row, remove bool) error {
	path := keysPath(loaded)
	rows, _, err := keyconf.Load(path)
	if err != nil {
		return err
	}
	switch {
	case remove && row.Chord == "":
		rows = nil
	case remove:
		rows = keyconf.Remove(rows, row.Chord, row.Mode)
	default:
		if diags := applyUserKeys(append(append([]keyconf.Row(nil), rows...), row)); len(diags) > 0 {
			applyUserKeys(rows)
			return fmt.Errorf("%s", diags[len(diags)-1].text)
		}
		rows = keyconf.Upsert(rows, row)
	}
	if err := keyconf.Save(path, rows); err != nil {
		return err
	}
	applyUserKeys(rows)
	fmt.Fprintf(stdout, "%s\n", path)
	return nil
}

// runDoctorKeys parses the file again and reports every row that will not
// take effect. Exits nonzero on any, so a shell can gate on it.
func runDoctorKeys(loaded *config.Loaded) error {
	path := keysPath(loaded)
	rows, warnings, err := keyconf.Load(path)
	if err != nil {
		return err
	}
	if len(rows) == 0 && len(warnings) == 0 {
		fmt.Fprintf(stdout, "%s: no keys.toml; the default map is in force\n", path)
		return nil
	}
	diags := applyUserKeys(rows)
	for _, w := range warnings {
		fmt.Fprintln(stdout, "  "+w)
	}
	for _, line := range diagLines() {
		fmt.Fprintln(stdout, "  "+line)
	}
	fmt.Fprintf(stdout, "%s: %d row(s), %d refused\n", path, len(rows), len(diags))
	if len(diags) > 0 || len(warnings) > 0 {
		return fmt.Errorf("keys.toml is not wholly in force")
	}
	return nil
}

// keysHelpLines is what `figaro keys` prints with no arguments in a terminal
// that wants the shape rather than the whole map.
func keysHelpLines() []string {
	return strings.Split(strings.TrimSpace(`
keys list              every live binding, by mode
keys actions           the vocabulary a binding may name
keys bind <chord> <action> [<modes>]
keys unbind <chord> [<modes>]
keys reset [<chord>]   drop the user's rows
keys path              where keys.toml lives`), "\n")
}
