package xwal

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/jack-work/figaro/figwal/disk"
)

type Retention struct {
	Node    string
	Channel string
	Pins    []uint64
	Dropped []disk.SealedSpan
}

func (r Retention) Bytes() int64 {
	var n int64
	for _, sp := range r.Dropped {
		n += sp.Bytes
	}
	return n
}

const minDroppableFiles = 3

func (t *Trunks) MayRetain(node, channel string) (bool, error) {
	n, err := disk.SegmentFiles(filepath.Join(t.root, channel, fsName(node)))
	return n >= minDroppableFiles, err
}

func (t *Trunks) Retain(node, channel string, dryRun bool) (Retention, error) {
	r := Retention{Node: node, Channel: channel}
	end, err := t.beginTrackedRead()
	if err != nil {
		return r, err
	}
	defer end()
	if t.node(node) == nil {
		return r, fmt.Errorf("%w %q", ErrUnknownTrunk, node)
	}
	if r.Pins, err = t.pinsOn(node, channel); err != nil {
		return r, err
	}
	x, err := t.openHotTopology(node)
	if err != nil {
		return r, err
	}
	defer x.Close()
	ch := x.channel(channel)
	if ch == nil {
		return r, fmt.Errorf("xwal: no channel %q", channel)
	}
	l, err := ch.Log()
	if err != nil {
		return r, err
	}
	if l.Absent() {
		return r, nil
	}
	keep := func(first, last uint64) bool {
		for _, p := range r.Pins {
			if p >= first && p <= last {
				return true
			}
		}
		return false
	}
	if dryRun {
		r.Dropped = l.Droppable(keep)
		return r, nil
	}
	r.Dropped, err = l.DropSealed(keep)
	return r, err
}

func (t *Trunks) pinsOn(node, channel string) ([]uint64, error) {
	var pins []uint64
	for _, child := range t.idx.ChildrenOf(node) {
		base, err := disk.ForkBaseOf(filepath.Join(t.root, channel, fsName(child)))
		if err != nil {
			return nil, fmt.Errorf("xwal: pin of %q on %q: %w", child, node, err)
		}
		if base > 1 && !slices.Contains(pins, base-1) {
			pins = append(pins, base-1)
		}
	}
	slices.Sort(pins)
	return pins, nil
}

func (x *XWAL) ChannelFloor(name string) uint64 {
	ch := x.channel(name)
	if ch == nil {
		return 0
	}
	l, err := ch.Log()
	if err != nil || l.Absent() {
		return 0
	}
	return l.Floor()
}
