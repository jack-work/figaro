package daemon

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

type pathSource string

const (
	pathFromConfig    pathSource = "config"
	pathFromManager   pathSource = "manager"
	pathFromInherited pathSource = "inherited"
)

type userManager func(xdgRuntimeDir string) (string, error)

func daemonPath(getenv func(string) string, configured []string, manager userManager) (string, pathSource, error) {
	if len(configured) > 0 {
		return strings.Join(configured, string(filepath.ListSeparator)), pathFromConfig, nil
	}
	own := getenv("PATH")
	xdg := getenv("XDG_RUNTIME_DIR")
	if xdg == "" || !isUsersOwnDaemon(getenv, xdg) {
		return own, pathFromInherited, nil
	}
	path, err := manager(xdg)
	if errors.Is(err, errNoUserManager) {
		err = nil
	}
	if err != nil || path == "" {
		return own, pathFromInherited, err
	}
	return path, pathFromManager, nil
}

func isUsersOwnDaemon(getenv func(string) string, xdg string) bool {
	rt := getenv("FIGARO_RUNTIME_DIR")
	return rt == "" || filepath.Clean(rt) == filepath.Join(xdg, "figaro")
}

func applyDaemonPath(configured []string) {
	path, source, err := daemonPath(os.Getenv, configured, userManagerPATH)
	if err != nil {
		slog.Warn("angelus PATH: the user manager's environment is unreadable, keeping the inherited PATH", "err", err)
	}
	if source != pathFromInherited {
		if err := os.Setenv("PATH", path); err != nil {
			slog.Error("angelus PATH: set", "source", source, "err", err)
			return
		}
	}
	slog.Info("angelus PATH", "source", string(source), "path", path)
}

var errNoUserManager = errors.New("no user manager")
