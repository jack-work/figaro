package store

import (
	"os"
	"path/filepath"
	"strings"
)

func (b *XwalBackend) LiveSidecarIDs() []string {
	ents, err := os.ReadDir(filepath.Join(b.root, metaDirName))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if meta, err := readJSON[AriaMeta](filepath.Join(b.root, metaDirName, e.Name())); err == nil && meta != nil && meta.TombstonedAtMS > 0 {
			continue
		}
		out = append(out, id)
	}
	return out
}
