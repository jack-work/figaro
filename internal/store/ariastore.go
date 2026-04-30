package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// AriaInfo holds metadata about a persisted aria. Returned by
// Backend.List so callers can enumerate arias without opening
// every handle.
type AriaInfo struct {
	ID           string
	MessageCount int
	LastModified time.Time
	Meta         *AriaMeta // nil if no metadata in file
}

// listAriasInDir scans a directory for aria subdirectories and returns
// metadata for each. An aria subdirectory is one that contains
// aria.jsonl. Used by FileBackend.List.
func listAriasInDir(dir string) ([]AriaInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var arias []AriaInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ariaDir := filepath.Join(dir, e.Name())
		ariaPath := filepath.Join(ariaDir, ariaFile)

		ariaStat, err := os.Stat(ariaPath)
		if err != nil {
			continue // skip dirs without aria.jsonl
		}

		// Count messages by counting lines in aria.jsonl. Cheap; doesn't
		// require parsing every entry.
		msgCount := 0
		if data, err := os.ReadFile(ariaPath); err == nil {
			scanner := bufio.NewScanner(bytes.NewReader(data))
			scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
			for scanner.Scan() {
				if len(scanner.Bytes()) > 0 {
					msgCount++
				}
			}
		}

		// meta.json is optional.
		var meta *AriaMeta
		if mdata, err := os.ReadFile(filepath.Join(ariaDir, metaFile)); err == nil {
			var m AriaMeta
			if json.Unmarshal(mdata, &m) == nil {
				meta = &m
			}
		}

		arias = append(arias, AriaInfo{
			ID:           e.Name(),
			MessageCount: msgCount,
			LastModified: ariaStat.ModTime(),
			Meta:         meta,
		})
	}

	return arias, nil
}
