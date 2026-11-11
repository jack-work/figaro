package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

const userManagerWait = 2 * time.Second

func userManagerPATH(xdgRuntimeDir string) (string, error) {
	socket := filepath.Join(xdgRuntimeDir, "systemd", "private")
	if !ownedSocket(socket, os.Getuid()) {
		return "", errNoUserManager
	}
	ctx, cancel := context.WithTimeout(context.Background(), userManagerWait)
	defer cancel()
	conn, err := dbus.Dial("unix:path="+socket, dbus.WithContext(ctx))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	authed := make(chan error, 1)
	go func() { authed <- conn.Auth([]dbus.Auth{dbus.AuthExternal(strconv.Itoa(os.Getuid()))}) }()
	select {
	case err := <-authed:
		if err != nil {
			return "", err
		}
	case <-ctx.Done():
		return "", fmt.Errorf("the user manager did not authenticate us in %s", userManagerWait)
	}
	var env dbus.Variant
	manager := conn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1")
	if err := manager.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, "org.freedesktop.systemd1.Manager", "Environment").Store(&env); err != nil {
		return "", err
	}
	vars, ok := env.Value().([]string)
	if !ok {
		return "", fmt.Errorf("the user manager's Environment is %s, not a string array", env.Signature())
	}
	return pathIn(vars), nil
}

func pathIn(environ []string) string {
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			return v
		}
	}
	return ""
}

func ownedSocket(path string, uid int) bool {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}
