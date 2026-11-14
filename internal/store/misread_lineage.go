package store

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/jack-work/figaro/figwal/disk"
	"github.com/jack-work/figaro/figwal/xwal"
)

type MisreadNode struct {
	Node     string   `json:"node"`
	Trunk    string   `json:"trunk,omitempty"`
	Kind     string   `json:"kind,omitempty"`
	Bases    []uint64 `json:"bases"`
	Channels []string `json:"channels"`
}

// MisreadLineages lists the nodes whose stored chain has a fork base below an
// earlier non-root ancestor's, and the derived channels each owns on disk.
// It opens files for reading only and takes no lock.
func MisreadLineages(root string) ([]MisreadNode, error) {
	layout, err := xwal.ReadLayout(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	bases := make(map[string]uint64, len(layout.Nodes))
	for key := range layout.Nodes {
		b, err := disk.ForkBaseOf(filepath.Join(root, layout.Main, xwal.NodeDirName(key)))
		if err != nil {
			return nil, fmt.Errorf("fork base of %s: %w", key, err)
		}
		bases[key] = b
	}
	var out []MisreadNode
	for key, info := range layout.Nodes {
		chain := layout.Lineage(key)
		var below []uint64
		for i := len(chain) - 2; i >= 0; i-- {
			below = append(below, bases[chain[i]])
		}
		if !basesFall(below) {
			continue
		}
		n := MisreadNode{Node: key, Trunk: info.Trunk, Kind: info.Kind, Bases: below}
		for _, ch := range layout.Channels {
			if !isDerivedChannel(ch) {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, ch, xwal.NodeDirName(key))); err == nil {
				n.Channels = append(n.Channels, ch)
			}
		}
		sort.Strings(n.Channels)
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out, nil
}

// basesFall reads the non-root bases root first, leaf last.
func basesFall(rootFirst []uint64) bool {
	var high uint64
	for _, b := range rootFirst {
		if b < high {
			return true
		}
		high = max(high, b)
	}
	return false
}

func isDerivedChannel(name string) bool {
	s, _, ok := schemaFor(name)
	return ok && s.class == classDerived
}

// clearMisreadDerived empties every derived channel of a node whose stored
// chain falls: those rows were encoded from a view that served an ancestor's
// records in place of the node's own.
func clearMisreadDerived(root string, trunks *xwal.Store) error {
	nodes, err := MisreadLineages(root)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if len(n.Channels) == 0 {
			continue
		}
		if n.Trunk == "" {
			slog.Warn("misread lineage: node has no trunk, derived rows left", "node", n.Node, "channels", n.Channels)
			continue
		}
		x, err := trunks.Head(n.Trunk)
		if err != nil {
			return fmt.Errorf("misread lineage %s: %w", n.Trunk, err)
		}
		err = func() error {
			defer x.Close()
			for _, ch := range n.Channels {
				if err := x.Clear(ch); err != nil {
					return fmt.Errorf("clear %s %s: %w", n.Trunk, ch, err)
				}
			}
			return nil
		}()
		if err != nil {
			return err
		}
		slog.Info("misread lineage: derived rows cleared", "node", n.Node, "trunk", n.Trunk, "channels", n.Channels)
	}
	return nil
}
