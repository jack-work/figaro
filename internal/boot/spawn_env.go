package boot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const AllowConfigDirEnv = "FIGARO_ALLOW_CONFIG_DIR"

func SpawnRefusal(getenv func(string) string) error {
	defaultRuntime := defaultRuntimeDir(getenv)
	if rt := getenv("FIGARO_RUNTIME_DIR"); rt != "" && filepath.Clean(rt) != filepath.Clean(defaultRuntime) {
		return nil
	}
	if v := strings.ToLower(strings.TrimSpace(getenv(AllowConfigDirEnv))); v == "1" || v == "true" || v == "yes" {
		return nil
	}
	home := getenv("HOME")
	var foreign []string
	for _, d := range []struct{ env, xdg, rel string }{
		{"FIGARO_CONFIG_DIR", "XDG_CONFIG_HOME", ".config"},
		{"FIGARO_STATE_DIR", "XDG_STATE_HOME", ".local/state"},
	} {
		set := getenv(d.env)
		if set == "" {
			continue
		}
		def := filepath.Join(home, d.rel, "figaro")
		if x := getenv(d.xdg); x != "" {
			def = filepath.Join(x, "figaro")
		}
		if filepath.Clean(set) != filepath.Clean(def) {
			foreign = append(foreign, fmt.Sprintf("%s=%s (the live daemon's is %s)", d.env, set, def))
		}
	}
	if len(foreign) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to start the daemon at %s, the live one, with %s\n"+
		"  unset it (env -u %s figaro ...), or set FIGARO_RUNTIME_DIR for a separate daemon,\n"+
		"  or set %s=1 if this is meant", defaultRuntime, strings.Join(foreign, " and "), strings.SplitN(foreign[0], "=", 2)[0], AllowConfigDirEnv)
}

func defaultRuntimeDir(getenv func(string) string) string {
	if d := getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "figaro")
	}
	return filepath.Join(os.TempDir(), "figaro")
}

func MissingConfigDir(getenv func(string) string) error {
	dir := getenv("FIGARO_CONFIG_DIR")
	if dir == "" {
		return nil
	}
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return nil
	}
	return fmt.Errorf("FIGARO_CONFIG_DIR=%s is not a directory: a daemon started on it would run with no outfits, no keepalive and no troupe; unset it or create it", dir)
}
