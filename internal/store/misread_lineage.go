package store

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/jack-work/figaro/figwal/xwal"
)

type MisreadNode struct {
	Node     string   `json:"node"`
	Trunk    string   `json:"trunk,omitempty"`
	Kind     string   `json:"kind,omitempty"`
	Bases    []uint64 `json:"bases"`
	Channels []string `json:"channels"`
	Meta     bool     `json:"meta"`
	Reason   string   `json:"reason"`
}

const MisreadFalling = "falling"

func MisreadLineages(root string) ([]MisreadNode, error) {
	layout, err := xwal.ReadLayout(root)
	if err != nil {
		return nil, err
	}
	bases := make(map[string]uint64, len(layout.Nodes))
	unreadable := map[string]error{}
	for key, info := range layout.Nodes {
		if info.From == "" {
			continue
		}
		b, err := layout.ForkBase(root, key)
		if err != nil {
			unreadable[key] = err
			continue
		}
		bases[key] = b
	}
	var out []MisreadNode
	for key, info := range layout.Nodes {
		chain := layout.Lineage(key)
		var below []uint64
		reason := ""
		for i := len(chain) - 2; i >= 0; i-- {
			if err, bad := unreadable[chain[i]]; bad && reason == "" {
				at := chain[i]
				if tr := layout.Nodes[at].Trunk; tr != "" {
					at = tr
				}
				reason = "unreadable .fork on " + at
				slog.Warn("misread lineage: unreadable fork base on the chain", "node", key, "at", chain[i], "err", err)
			}
			below = append(below, bases[chain[i]])
		}
		if top := layout.Nodes[chain[len(chain)-1]]; reason == "" && top.From != "" {
			if _, held := layout.Nodes[top.From]; !held {
				reason = "missing parent " + top.From
				slog.Warn("misread lineage: the chain's top forks from a node the store does not hold", "node", key, "parent", top.From)
			}
		}
		if reason == "" && basesFall(below) {
			reason = MisreadFalling
		}
		if reason == "" {
			continue
		}
		n := MisreadNode{Node: key, Trunk: info.Trunk, Kind: info.Kind, Bases: below, Reason: reason}
		for _, ch := range layout.Channels {
			if !isDerivedChannel(ch) {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, ch, xwal.NodeDirName(key))); err == nil {
				n.Channels = append(n.Channels, ch)
			}
		}
		sort.Strings(n.Channels)
		if info.Trunk != "" {
			if _, err := os.Stat(metaSidecar(root, info.Trunk)); err == nil {
				n.Meta = true
			}
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out, nil
}

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

func clearMisreadDerived(root string, trunks *xwal.Store) error {
	nodes, err := MisreadLineages(root)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if n.Meta {
			if err := markMetaRefold(metaSidecar(root, n.Trunk)); err != nil {
				return fmt.Errorf("misread lineage %s: mark meta: %w", n.Trunk, err)
			}
		}
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

func metaSidecar(root, id string) string { return filepath.Join(root, "_meta", id+".json") }

func markMetaRefold(path string) error {
	meta, err := readJSON[AriaMeta](path)
	if err != nil || meta == nil {
		return err
	}
	meta.Refold = true
	return writeJSON(path, meta)
}
