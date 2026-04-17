package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// listAriasInDir scans a directory for aria JSON files and returns
// metadata for each. The aria ID is derived from the filename
// (minus the .json extension). Used by FileBackend.List.
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
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		id := strings.TrimSuffix(e.Name(), ".json")
		path := filepath.Join(dir, e.Name())

		info, err := e.Info()
		if err != nil {
			continue // skip unreadable entries
		}

		// Read just enough to count messages and get metadata.
		msgCount := 0
		var meta *AriaMeta
		if data, err := os.ReadFile(path); err == nil {
			var fd fileData
			if json.Unmarshal(data, &fd) == nil {
				msgCount = len(fd.Messages)
				meta = fd.Meta
			}
		}

		arias = append(arias, AriaInfo{
			ID:           id,
			MessageCount: msgCount,
			LastModified: info.ModTime(),
			Meta:         meta,
		})
	}

	return arias, nil
}
