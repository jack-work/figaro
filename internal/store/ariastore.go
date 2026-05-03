package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// AriaInfo is what `figaro list` shows for a persisted aria. Read
// from meta.json + the aria.jsonl mtime; we don't open aria.jsonl.
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
		ariaDir := filepath.Join(dir, e.Name())
		ariaStat, err := os.Stat(filepath.Join(ariaDir, ariaFile))
		if err != nil {
			continue
		}
		var meta *AriaMeta
		if mdata, err := os.ReadFile(filepath.Join(ariaDir, metaFile)); err == nil {
			var m AriaMeta
			if json.Unmarshal(mdata, &m) == nil {
				meta = &m
			}
		}
		info := AriaInfo{
			ID:           e.Name(),
			LastModified: ariaStat.ModTime(),
			Meta:         meta,
		}
		if meta != nil {
			info.MessageCount = meta.MessageCount
		}
		arias = append(arias, info)
	}
	return arias, nil
}
