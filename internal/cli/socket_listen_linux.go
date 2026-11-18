//go:build linux

package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const unixAcceptCon = 0x10000

func socketListening(path string) (known, listening bool) {
	data, err := os.ReadFile("/proc/net/unix")
	if err != nil {
		return false, false
	}
	return true, unixSocketListeningIn(string(data), path)
}

func unixSocketListeningIn(table, path string) bool {
	candidates := socketPathCandidates(path)
	lines := strings.Split(table, "\n")
	for i := 1; i < len(lines); i++ {
		flagsHex, entryPath, ok := parseProcNetUnixLine(lines[i])
		if !ok || !matchesAnyPath(entryPath, candidates) {
			continue
		}
		flags, err := strconv.ParseUint(flagsHex, 16, 64)
		if err == nil && flags&unixAcceptCon != 0 {
			return true
		}
	}
	return false
}

func socketPathCandidates(path string) []string {
	clean := filepath.Clean(path)
	out := []string{clean}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(clean)); err == nil {
		if resolved := filepath.Join(dir, filepath.Base(clean)); resolved != clean {
			out = append(out, resolved)
		}
	}
	if full, err := filepath.EvalSymlinks(clean); err == nil && !matchesAnyPath(full, out) {
		out = append(out, full)
	}
	return out
}

func matchesAnyPath(p string, candidates []string) bool {
	for _, c := range candidates {
		if p == c {
			return true
		}
	}
	return false
}

func parseProcNetUnixLine(line string) (flagsHex, path string, ok bool) {
	s := strings.TrimRight(line, "\r\n")
	pos := 0
	for field := 0; field < 7; field++ {
		for pos < len(s) && (s[pos] == ' ' || s[pos] == '\t') {
			pos++
		}
		start := pos
		for pos < len(s) && s[pos] != ' ' && s[pos] != '\t' {
			pos++
		}
		if start == pos {
			return "", "", false
		}
		if field == 3 {
			flagsHex = s[start:pos]
		}
	}
	for pos < len(s) && (s[pos] == ' ' || s[pos] == '\t') {
		pos++
	}
	if pos >= len(s) {
		return "", "", false
	}
	return flagsHex, s[pos:], true
}
