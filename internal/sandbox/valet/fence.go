package valet

import (
	"io"
	"path/filepath"
	"slices"
	"time"
)

type Valet struct {
	Peers    []int
	State    string
	MaxGrant time.Duration
	Home     string
	Door     string
	Hide     []string
	Self     string
	Log      io.Writer
}

type Mount struct {
	Path  string `json:"path"`
	Write bool   `json:"write,omitempty"`
}

type Fence struct {
	Root    string   `json:"root"`
	Home    string   `json:"home"`
	Scratch string   `json:"scratch"`
	Door    string   `json:"door,omitempty"`
	Grants  []Mount  `json:"grants,omitempty"`
	Hide    []string `json:"hide,omitempty"`
}

func (f Fence) ordered() []Mount {
	out := slices.Clone(f.Grants)
	slices.SortStableFunc(out, func(a, b Mount) int {
		return len(filepath.Clean(a.Path)) - len(filepath.Clean(b.Path))
	})
	return out
}

func GrantMounts(gs []Grant) []Mount {
	byPath := map[string]int{}
	var out []Mount
	for _, g := range gs {
		if i, ok := byPath[g.Path]; ok {
			out[i].Write = out[i].Write || g.Write()
			continue
		}
		byPath[g.Path] = len(out)
		out = append(out, Mount{Path: g.Path, Write: g.Write()})
	}
	return out
}
