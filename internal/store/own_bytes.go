package store

import (
	"os"
	"path/filepath"
	"strings"
)

func (b *XwalBackend) OwnBytes(id string) int64 {
	return b.store.trunks.TrunkBytes(id, b.ownChannels())
}

func (b *XwalBackend) ownChannels() []string {
	out := []string{chanIR, chanForm, chanUI}
	ents, err := os.ReadDir(filepath.Join(b.root, filepath.FromSlash(translationPrefix)))
	if err != nil {
		return out
	}
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, translationPrefix+strings.TrimSuffix(e.Name(), "/"))
		}
	}
	return out
}
