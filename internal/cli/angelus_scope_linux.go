package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const scopeMoveWait = 5 * time.Second

func moveIntoScope(p angelusPlacement, pid int) (string, error) {
	name := p.scopeFor(pid)
	ctx, cancel := context.WithTimeout(context.Background(), scopeMoveWait)
	defer cancel()
	conn, err := dbus.Dial("unix:path="+p.managerSocket, dbus.WithContext(ctx))
	if err != nil {
		return name, err
	}
	defer conn.Close()
	authed := make(chan error, 1)
	go func() { authed <- conn.Auth([]dbus.Auth{dbus.AuthExternal(strconv.Itoa(os.Getuid()))}) }()
	select {
	case err := <-authed:
		if err != nil {
			return name, err
		}
	case <-ctx.Done():
		conn.Close()
		return name, fmt.Errorf("the user manager did not authenticate us in %s", scopeMoveWait)
	}
	props := []struct {
		Name  string
		Value dbus.Variant
	}{
		{"PIDs", dbus.MakeVariant([]uint32{uint32(pid)})},
		{"Description", dbus.MakeVariant("figaro angelus")},
		{"CollectMode", dbus.MakeVariant("inactive-or-failed")},
	}
	aux := []struct {
		Name  string
		Props []struct {
			Name  string
			Value dbus.Variant
		}
	}{}
	manager := conn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1")
	if err := manager.CallWithContext(ctx, "org.freedesktop.systemd1.Manager.StartTransientUnit", 0, name, "fail", props, aux).Err; err != nil {
		return name, err
	}
	for {
		if inScope(pid, name) {
			return name, nil
		}
		select {
		case <-ctx.Done():
			return name, fmt.Errorf("%d was not in %s after %s", pid, name, scopeMoveWait)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func inScope(pid int, scope string) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	return err == nil && strings.Contains(string(raw), "/"+scope+"\n")
}
