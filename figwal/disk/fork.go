package disk

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// forkMarkerName records the global log index this fork begins at; the
// parent is always the immediate parent directory.
const forkMarkerName = ".fork"

// ForkPendingName signals a fork that crashed before completing: Open
// refuses, and the recovery layer consumes it.
const ForkPendingName = ".fork-pending"

var ErrForkPending = errors.New("fork in progress: dir contains .fork-pending sentinel")

// readForkMarker returns (0, nil) when .fork is absent: dir is a root.
func readForkMarker(dir string) (uint64, error) {
	path := filepath.Join(dir, forkMarkerName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	// key=value lines; today only base=<N>.
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return 0, fmt.Errorf("malformed .fork line: %q", line)
		}
		switch strings.TrimSpace(k) {
		case "base":
			n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return 0, fmt.Errorf("malformed .fork base: %w", err)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf(".fork present but missing base= field")
}

func hasForkPending(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, ForkPendingName))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// Subdirectory presence is what marks a log as a branch point and therefore read-only.
func hasSubdirs(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		return true, nil
	}
	return false, nil
}
