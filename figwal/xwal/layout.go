package xwal

import (
	"fmt"
	"os"
	"path/filepath"
)

type Layout struct {
	Main     string
	Channels []string
	Nodes    map[string]NodeInfo
}

func ReadLayout(root string) (Layout, error) {
	m, err := readManifestFile(root)
	if err != nil {
		return Layout{}, err
	}
	if m.Layout != layoutVersion {
		return Layout{}, fmt.Errorf("xwal: %s has layout %d, want %d", root, m.Layout, layoutVersion)
	}
	l := Layout{Main: m.Main, Nodes: map[string]NodeInfo{}}
	for _, c := range m.Channels {
		l.Channels = append(l.Channels, c.Name)
	}
	entries, err := os.ReadDir(filepath.Join(root, m.Main))
	if err != nil {
		return Layout{}, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		marker, ok := readNodeMarker(filepath.Join(root, m.Main, e.Name()))
		if !ok {
			continue
		}
		l.Nodes[keyName(e.Name())] = NodeInfo{Trunk: marker.trunk, From: marker.from, Kind: marker.kind, Placed: marker.placed, PlacedTop: marker.placedTop}
	}
	return l, nil
}

func (l Layout) Lineage(key string) []string {
	var out []string
	seen := map[string]bool{}
	for key != "" && !seen[key] {
		n, ok := l.Nodes[key]
		if !ok {
			break
		}
		seen[key] = true
		out = append(out, key)
		key = n.From
	}
	return out
}

func NodeDirName(key string) string { return fsName(key) }
