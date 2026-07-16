//go:build windows

package tool

import (
	"os"
	"os/exec"
	"path/filepath"
)

// bashPath returns the path to Git Bash on Windows, preferring it over
// WSL's bash relay (C:\Windows\System32\bash.exe) which is fragile and
// not the POSIX shell figaro expects. Falls back to bare "bash" if Git
// Bash isn't found.
func bashPath() string {
	// Check common Git Bash locations.
	for _, candidate := range gitBashCandidates() {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// Fall back to PATH resolution (might hit WSL relay).
	if p, err := exec.LookPath("bash.exe"); err == nil {
		return p
	}
	return "bash"
}

func gitBashCandidates() []string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	return []string{
		filepath.Join(pf, "Git", "bin", "bash.exe"),
		filepath.Join(pf, "Git", "usr", "bin", "bash.exe"),
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\msys64\usr\bin\bash.exe`,
	}
}
