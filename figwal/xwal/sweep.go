package xwal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jack-work/figaro/figwal/disk"
)

type SweepReport struct {
	Unmarked []string
	Removed  int
	Skipped  string
}

func plannedNodes(dir string) (map[string]bool, error) {
	keep := map[string]bool{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		name := e.Name()
		removal := strings.HasPrefix(name, removalPrefix)
		if !removal && !strings.HasPrefix(name, graftPrefix) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name, graftPlanName))
		if err != nil {
			return nil, fmt.Errorf("plan %s unreadable: %w", name, err)
		}
		var plan graftPlan
		if removal {
			err = json.Unmarshal(body, &plan.Component)
		} else {
			err = json.Unmarshal(body, &plan)
		}
		if err != nil {
			return nil, fmt.Errorf("plan %s unparsable: %w", name, err)
		}
		for _, n := range plan.Component.Nodes {
			keep[n.Key] = true
		}
		for _, k := range plan.Keys {
			keep[k] = true
		}
	}
	return keep, nil
}

func absent(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	return false, err
}

func (s *Store) SweepUnmarkedNodes(remove bool) (SweepReport, error) {
	t := s.Trunks
	if s.lockFile == nil {
		return SweepReport{Skipped: "the store's writer lock is not held"}, nil
	}
	if n := len(peersOf(t.registryRoot)); n != 1 {
		return SweepReport{Skipped: fmt.Sprintf("%d openers of this store in the process", n)}, nil
	}
	if err := t.lockCurrent(); err != nil {
		return SweepReport{}, err
	}
	defer t.mu.Unlock()
	keep, err := plannedNodes(t.root)
	if err != nil {
		slog.Warn("xwal: node sweep skipped, every directory kept", "reason", err)
		return SweepReport{Skipped: err.Error()}, nil
	}
	names, err := channelNames(t.root)
	if err != nil {
		return SweepReport{}, err
	}
	channelPath := map[string]bool{}
	for _, ch := range names {
		for p := ch; p != "." && p != "/" && p != ""; p = filepath.Dir(p) {
			channelPath[filepath.ToSlash(p)] = true
		}
	}
	var report SweepReport
	for _, ch := range names {
		chDir := filepath.Join(t.root, ch)
		ents, err := os.ReadDir(chDir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			slog.Warn("xwal: node sweep skipped, every directory kept", "reason", err)
			return SweepReport{Skipped: err.Error()}, nil
		}
		swept := false
		for _, e := range ents {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || keep[keyName(e.Name())] || channelPath[filepath.ToSlash(filepath.Join(ch, e.Name()))] {
				continue
			}
			path := filepath.Join(chDir, e.Name())
			noMarker, err := absent(filepath.Join(t.root, t.main, e.Name(), nodeMarkerName))
			if err != nil {
				slog.Warn("xwal: a node's marker cannot be read; its directory is kept", "path", path, "err", err)
				continue
			}
			if !noMarker {
				continue
			}
			noFork, err := absent(filepath.Join(path, ".fork"))
			if err != nil || noFork {
				slog.Warn("xwal: a directory with no node marker and no readable fork base is kept", "path", path)
				continue
			}
			report.Unmarked = append(report.Unmarked, path)
			if !remove {
				slog.Info("xwal: node sweep (dry run) would remove a directory with no marker and no plan", "path", path)
				continue
			}
			if err := os.RemoveAll(path); err != nil {
				return report, err
			}
			report.Removed++
			swept = true
			slog.Info("xwal: node sweep removed a directory with no marker and no plan", "path", path)
		}
		if swept {
			if err := disk.SyncDir(chDir); err != nil {
				return report, err
			}
		}
	}
	slog.Info("xwal: node sweep", "unmarked", len(report.Unmarked), "removed", report.Removed, "dry_run", !remove)
	return report, nil
}
