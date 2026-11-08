package xwal

import (
	"os"
	"path/filepath"
)

func (t *Trunks) TrunkBytes(trunk string, channels []string) int64 {
	end, err := t.beginTrackedRead()
	if err != nil {
		return 0
	}
	defer end()
	var n int64
	for key, node := range t.idx.All() {
		if node.Trunk != trunk {
			continue
		}
		for _, ch := range channels {
			n += dirBytes(filepath.Join(t.root, filepath.FromSlash(ch), fsName(key)))
		}
	}
	return n
}

func dirBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			n += info.Size()
		}
		return nil
	})
	return n
}
