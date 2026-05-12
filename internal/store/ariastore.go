package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// AriaInfo is persisted aria metadata for `figaro list`.
type AriaInfo struct {
	ID           string
	MessageCount int
	LastModified time.Time
	Meta         *AriaMeta // nil if no meta.json
}

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
		ariaRoot := filepath.Join(dir, e.Name())
		// Either layout counts as an aria: legacy aria.jsonl file, or
		// figwal aria/ subdir.
		var modTime time.Time
		if st, err := os.Stat(filepath.Join(ariaRoot, ariaFile)); err == nil {
			modTime = st.ModTime()
		} else if st, err := os.Stat(filepath.Join(ariaRoot, ariaDir)); err == nil && st.IsDir() {
			modTime = st.ModTime()
		} else {
			continue
		}
		var meta *AriaMeta
		if mdata, err := os.ReadFile(filepath.Join(ariaRoot, metaFile)); err == nil {
			var m AriaMeta
			if json.Unmarshal(mdata, &m) == nil {
				meta = &m
			}
		}
		info := AriaInfo{
			ID:           e.Name(),
			LastModified: modTime,
			Meta:         meta,
		}
		if meta != nil {
			info.MessageCount = meta.MessageCount
		}
		arias = append(arias, info)
	}
	return arias, nil
}
