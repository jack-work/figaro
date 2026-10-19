package auth

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func openBrowser(u string) {
	for _, argv := range browserCommands(os.Getenv("BROWSER"), runtime.GOOS, u) {
		cmd := exec.Command(argv[0], argv[1:]...)
		if cmd.Start() == nil {
			go cmd.Wait()
			return
		}
	}
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
