package testfence

import (
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"
	"github.com/zalando/go-keyring"
)

const DeadSessionBus = "unix:path=/nonexistent/figaro-tests-have-no-session-bus"

var inheritedVault = []string{"FIGARO_HUSH_DIR", "FIGARO_HUSH_PASSPHRASE"}

func Install() {
	if err := os.Setenv("DBUS_SESSION_BUS_ADDRESS", DeadSessionBus); err != nil {
		panic(err)
	}
	for _, k := range inheritedVault {
		if err := os.Unsetenv(k); err != nil {
			panic(err)
		}
	}
	keyring.MockInit()
}

func Sealed() error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil
	}
	conn.Close()
	return fmt.Errorf("a session bus answers at DBUS_SESSION_BUS_ADDRESS=%q, so the OS keyring is reachable", os.Getenv("DBUS_SESSION_BUS_ADDRESS"))
}
