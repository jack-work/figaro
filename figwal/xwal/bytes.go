package xwal

import (
	"os"
	"path/filepath"
)

func (t *Trunks) TrunkBytes(trunk string, channels []string) int64 {
	got, _ := t.TrunksBytes([]string{trunk}, channels)
	return got[trunk]
}

func (t *Trunks) TrunksBytes(trunks []string, channels []string) (map[string]int64, error) {
	want := make(map[string]bool, len(trunks))
	for _, tr := range trunks {
		want[tr] = true
	}
	end, err := t.beginTrackedRead()
	if err != nil {
		return nil, err
	}
	defer end()
	out := make(map[string]int64, len(trunks))
	for _, tr := range trunks {
		out[tr] = 0
	}
	for key, node := range t.idx.All() {
		if !want[node.Trunk] {
			continue
		}
		for _, ch := range channels {
			out[node.Trunk] += dirBytes(filepath.Join(t.root, filepath.FromSlash(ch), fsName(key)))
		}
	}
	return out, nil
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
