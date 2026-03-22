package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AriaInfo holds metadata about a persisted aria on disk.
type AriaInfo struct {
	ID           string
	MessageCount int
	LastModified time.Time
}

// ListArias scans a directory for aria JSON files and returns
// metadata for each. The aria ID is derived from the filename
// (minus the .json extension).
func ListArias(dir string) ([]AriaInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var arias []AriaInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		id := strings.TrimSuffix(e.Name(), ".json")
		path := filepath.Join(dir, e.Name())

		info, err := e.Info()
		if err != nil {
			continue // skip unreadable entries
		}

		// Read just enough to count messages.
		msgCount := 0
		if data, err := os.ReadFile(path); err == nil {
			var fd fileData
			if json.Unmarshal(data, &fd) == nil {
				msgCount = len(fd.Messages)
			}
		}

		arias = append(arias, AriaInfo{
			ID:           id,
			MessageCount: msgCount,
			LastModified: info.ModTime(),
		})
	}

	return arias, nil
}

// RemoveAria deletes an aria file from disk by ID.
// Returns nil if the file does not exist.
func RemoveAria(dir, id string) error {
	path := filepath.Join(dir, id+".json")
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
