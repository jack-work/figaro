package xwal

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jack-work/figaro/figwal/disk"
	"github.com/jack-work/figaro/figwal/log"
	"github.com/jack-work/figaro/figwal/segment"
)

// ErrAbsorbConflict: a channel holds a segment below its fork base that is
// not the prefix absorbing from its parent builds. Nothing is replaced.
var ErrAbsorbConflict = errors.New("xwal: a segment below the fork base is not the parent's prefix")

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

func absorbScratch(dir string) string {
	return filepath.Join(dir, fmt.Sprintf(".absorb-%d", os.Getpid()))
}

func absorbPrefix(dir string, src *log.Log, base uint64, initial []byte, codec segment.SegmentCodec) error {
	if base <= 1 {
		return nil
	}
	tmpName := absorbScratch(dir)
	defer os.Remove(tmpName)
	from, err := buildAbsorbed(tmpName, src, base, initial, codec)
	if err != nil {
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

// buildAbsorbed writes the segment that holds src's records [from, base)
// to path, and returns from. path is a scratch name: segment.Create is
// O_EXCL, so a partial file at the final name would make every retry fail.
func buildAbsorbed(path string, src *log.Log, base uint64, initial []byte, codec segment.SegmentCodec) (uint64, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	from, header := uint64(1), initial
	if initial != nil && src.Floor() > 0 {
		h, segBase, err := src.SegmentHeaderAt(base - 1)
		if err != nil {
			return 0, err
		}
		from, header = segBase, h
	}
	// maxSize 0: unbounded, so the whole prefix lands in this one segment.
	seg, err := segment.Create(path, codec, from, 0)
	if err != nil {
		return 0, err
	}
	if header != nil {
		if err := seg.WriteHeader(header); err != nil {
			seg.Close()
			return 0, err
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
		return 0, rerr
	}
	if err := seg.Sync(); err != nil {
		seg.Close()
		return 0, err
	}
	return from, seg.Close()
}

// finishInterruptedAbsorb runs on every open of a node's channel. A Detach
// that died after absorbing the channel's prefix and before its .fork write
// leaves a segment below the base; the log takes appends into it, and a
// later absorb would land on it. If the segment starts with exactly what
// absorb builds from the parent now, the absorb is finished by writing
// .fork base=1; otherwise the open refuses and nothing is rewritten. The
// node's .node is left to the Detach re-run, which also rebuilds topology.
func finishInterruptedAbsorb(dir string, l *log.Log, initial []byte, codec segment.SegmentCodec) error {
	base := l.ForkBase()
	if base <= 1 {
		return nil
	}
	if segs := l.SegmentBaseIndexes(); len(segs) == 0 || segs[0] >= base {
		return nil
	}
	own, err := segmentBases(dir, codec)
	if err != nil {
		return err
	}
	if len(own) == 0 {
		return fmt.Errorf("%w: %s base %d: only a hole below the base", ErrAbsorbConflict, dir, base)
	}
	first := own[0]
	tmp := absorbScratch(dir)
	defer os.Remove(tmp)
	from, err := buildAbsorbed(tmp, l, base, initial, codec)
	if err != nil {
		return err
	}
	name := segFileName(first, codec)
	if first != from {
		return fmt.Errorf("%w: %s base %d segment %s, absorb starts at %d", ErrAbsorbConflict, dir, base, name, from)
	}
	ok, err := fileHasPrefix(filepath.Join(dir, name), tmp)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s base %d segment %s", ErrAbsorbConflict, dir, base, name)
	}
	if err := writeForkMarker(dir, 1); err != nil {
		return err
	}
	l.AdoptAbsorbedPrefix()
	return nil
}

func fileHasPrefix(path, prefixPath string) (bool, error) {
	want, err := os.ReadFile(prefixPath)
	if err != nil {
		return false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	got := make([]byte, len(want))
	n, err := f.ReadAt(got, 0)
	if n < len(want) {
		if err == nil || errors.Is(err, io.EOF) {
			return false, nil
		}
		return false, err
	}
	return bytes.Equal(got, want), nil
}
