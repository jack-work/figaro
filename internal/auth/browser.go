package auth

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const NoBrowserEnv = "FIGARO_NO_BROWSER"

func BrowserAllowed() bool {
	return browserAllowed(os.Getenv, runtime.GOOS)
}

func browserAllowed(getenv func(string) string, goos string) bool {
	if getenv(NoBrowserEnv) != "" {
		return false
	}
	switch goos {
	case "darwin", "windows":
		return true
	}
	return getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""
}

func OpenBrowser(u string) bool {
	if !BrowserAllowed() {
		return false
	}
	for _, argv := range browserCommands(os.Getenv("BROWSER"), runtime.GOOS, u) {
		cmd := exec.Command(argv[0], argv[1:]...)
		if cmd.Start() == nil {
			go cmd.Wait()
			return true
		}
	}
	return false
}

func browserCommands(browser, goos, u string) [][]string {
	var out [][]string
	if argv := strings.Fields(browser); len(argv) > 0 {
		out = append(out, withURL(argv, u))
	}
	switch goos {
	case "darwin":
		out = append(out, []string{"open", u})
	case "windows":
		out = append(out, []string{"rundll32", "url.dll,FileProtocolHandler", u})
	default:
		out = append(out, []string{"xdg-open", u})
	}
	return out
}

func withURL(argv []string, u string) []string {
	placed := false
	for i, a := range argv {
		if strings.Contains(a, "%s") {
			argv[i], placed = strings.ReplaceAll(a, "%s", u), true
		}
	}
	if !placed {
		argv = append(argv, u)
	}
	return argv
}
