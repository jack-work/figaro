package telemetrydir

import (
	"os"
	"path/filepath"
)

// For returns where this CLI process writes its telemetry files.
//
// It is want (the configured telemetry dir, by default the shared state dir)
// when this user can write there. A client of a daemon that runs as another
// user shares that daemon's state dir through FIGARO_STATE_DIR but usually
// cannot write it, so every command used to print "otel init: ... permission
// denied". Then the CLI writes to the user's own state dir instead, silently:
// telemetry is best effort and the warning helped no one.
func For(want string, getenv func(string) string) string {
	if dirWritable(want) {
		return want
	}
	own := userStateDir(getenv)
	if own == "" || own == want || !dirWritable(own) {
		return want // let otel report the original error
	}
	return own
}

// userStateDir is the user's own figaro state dir, ignoring FIGARO_STATE_DIR.
func userStateDir(getenv func(string) string) string {
	if d := getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "figaro")
	}
	home := getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "state", "figaro")
}

// dirWritable reports whether this process can create the dir and append to
// the telemetry files in it. It tries the operations themselves rather than
// reading mode bits, so ACLs, read-only mounts and foreign owners all count.
func dirWritable(dir string) bool {
	if dir == "" || os.MkdirAll(dir, 0o700) != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".figaro-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	for _, n := range []string{"traces.jsonl", "metrics.jsonl", "logs.jsonl"} {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		g, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return false
		}
		g.Close()
	}
	return true
}
