package xwal

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jack-work/figaro/figwal/disk"
	"github.com/jack-work/figaro/figwal/log"
	"github.com/jack-work/figaro/figwal/segment"
)

// Detach makes a node self-sufficient: it absorbs the history prefix it
// currently reads through an ancestor, and stops pointing at one.
func (t *Trunks) Detach(node string) error {
	if node == "" {
		return fmt.Errorf("xwal: cannot detach the root")
	}
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return err
	}
	defer endMutation()

	x, err := t.openHotTopology(node)
	if err != nil {
		return err
	}
	var dirs []string
	for _, c := range x.Channels() {
		ch := x.channel(c.Name)
		dir := filepath.Join(t.root, c.Name, fsName(node))
		if _, serr := os.Stat(dir); os.IsNotExist(serr) {
			continue // a channel added after this node existed
		} else if serr != nil {
			x.Close()
			return serr
		}
		dirs = append(dirs, dir)
		chLog, lerr := ch.Log()
		if lerr != nil {
			x.Close()
			return lerr
		}
		base := chLog.ForkBase()
		if base <= 1 {
			continue // already its own root in this channel
		}
		var initial []byte
		if ch.kind == ChannelReducible {
			initial = ch.initial
		}
		if err := absorbPrefix(dir, chLog, base, initial, x.codec); err != nil {
			x.Close()
			return fmt.Errorf("xwal: detach %q channel %q: %w", node, c.Name, err)
		}
	}
	x.Close()

	// Publish .fork per channel, then .from last. A crash between two
	// channels' .fork writes leaves each channel correct because .from still
	// names the parent: a flipped channel reads its absorbed copy, an
	// unflipped one delegates, and the two are byte-identical. Clearing .from
	// first would strand every channel not yet flipped.
	for _, dir := range dirs {
		if err := writeForkMarker(dir, 1); err != nil {
			return err
		}
	}
	kind, trunk := "conversation", ""
	if n := t.node(node); n != nil {
		if n.Kind != "" {
			kind = n.Kind
		}
		trunk = n.Trunk
	}
	if err := writeNodeMarker(t.irDir(node), nodeMarker{kind: kind, trunk: trunk}); err != nil {
		return err
	}
	return t.rebuild()
}

func absorbPrefix(dir string, src *log.Log, base uint64, initial []byte, codec segment.SegmentCodec) error {
	if base <= 1 {
		return nil
	}
	// Build under a scratch name and rename: segment.Create is O_EXCL, so a
	// partial file at the final name would make every retry fail with EEXIST,
	// and crash recovery here is re-running Detach.
	tmpName := filepath.Join(dir, fmt.Sprintf(".absorb-%d", os.Getpid()))
	if err := os.Remove(tmpName); err != nil && !os.IsNotExist(err) {
		return err
	}
	defer os.Remove(tmpName)
	from, header := uint64(1), initial
	if initial != nil && src.Floor() > 0 {
		h, segBase, err := src.SegmentHeaderAt(base - 1)
		if err != nil {
			return err
		}
		from, header = segBase, h
	}
	// maxSize 0: unbounded, so the whole prefix lands in this one segment.
	seg, err := segment.Create(tmpName, codec, from, 0)
	if err != nil {
		return err
	}
	if header != nil {
		if err := seg.WriteHeader(header); err != nil {
			seg.Close()
			return err
		}
	}
	rerr := src.Range(from, func(idx uint64, payload []byte) error {
		if idx >= base {
			return errStopRange
		}
		_, aerr := seg.Append(payload)
		return aerr
	})
	if rerr != nil && rerr != errStopRange {
		seg.Close()
		return rerr
	}
	if err := seg.Sync(); err != nil {
		seg.Close()
		return err
	}
	if err := seg.Close(); err != nil {
		return err
	}
	if from > 1 {
		if err := disk.StubHole(dir, segFileName(1, codec)); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpName, filepath.Join(dir, segFileName(from, codec))); err != nil {
		return err
	}
	return disk.SyncDir(dir)
}
