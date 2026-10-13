//go:build windows

package local

import (
	"os"
	"os/exec"
	"path/filepath"
)

func bashPath() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	for _, candidate := range []string{
		filepath.Join(pf, "Git", "bin", "bash.exe"),
		filepath.Join(pf, "Git", "usr", "bin", "bash.exe"),
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\msys64\usr\bin\bash.exe`,
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if p, err := exec.LookPath("bash.exe"); err == nil {
		return p
	}
	return "bash"
}
