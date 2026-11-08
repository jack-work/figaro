package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

type angelusPlacement struct {
	managerSocket string
	scopePrefix   string
}

func placeAngelus(getenv func(string) string, managerAt func(string) bool, runtimeDir string) (angelusPlacement, bool) {
	xdg := getenv("XDG_RUNTIME_DIR")
	if xdg == "" || envTruthy(getenv("FIGARO_NO_ANGELUS_SCOPE")) {
		return angelusPlacement{}, false
	}
	socket := filepath.Join(xdg, "systemd", "private")
	if !managerAt(socket) {
		return angelusPlacement{}, false
	}
	sum := sha256.Sum256([]byte(runtimeDir))
	prefix := "figaro-angelus-" + hex.EncodeToString(sum[:4])
	if getenv("FIGARO_DEV_ROOT") != "" {
		prefix = "figaro-angelus-dev-" + hex.EncodeToString(sum[:4])
	}
	return angelusPlacement{managerSocket: socket, scopePrefix: prefix}, true
}

func (p angelusPlacement) scopeFor(pid int) string {
	return fmt.Sprintf("%s-%d.scope", p.scopePrefix, pid)
}

func socketExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}
