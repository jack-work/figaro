package xwal

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jack-work/figaro/internal/store/disk"
	"github.com/jack-work/figaro/internal/store/log"
	"github.com/jack-work/figaro/internal/store/segment"
)

// Detach makes a node self-sufficient: it absorbs the history prefix it
// currently reads through an ancestor, and stops pointing at one.
//
// This is what a delete owes a BOUNDARY SURVIVOR — an aria outside the
// delete set whose lineage runs through it. Once every such survivor has
// detached, the doomed directories carry nothing anyone still reads and can
// be unlinked.
//
// Crash-safe and idempotent by ordering, with no journal: the absorbed
// records are written FIRST, as segments below the node's own fork base,
// where every read still delegates to the ancestor and so cannot see them.
// Only the marker flip publishes them, and that is a rename. Crash before
// it and the node still reads through its ancestor, with some ignored files
// that the next Detach overwrites; crash after and it is independent.
//
// Copy, not adopt: it touches nothing the ancestor owns and nothing the
// node's own writer is using, so it needs no quiesce. Adopting the
// ancestor's directory would be cheaper in bytes and would require one.
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
		ch := x.chans[c.Name]
		dir := filepath.Join(t.root, c.Name, fsName(node))
		if _, serr := os.Stat(dir); os.IsNotExist(serr) {
			continue // a channel added after this node existed
		} else if serr != nil {
			x.Close()
			return serr
		}
		dirs = append(dirs, dir)
		base := ch.log.ForkBase()
		if base <= 1 {
			continue // already its own root in this channel
		}
		var initial []byte
		if ch.kind == ChannelReducible {
			initial = ch.initial
		}
		if err := absorbPrefix(dir, ch.log, base, initial, x.codec); err != nil {
			x.Close()
			return fmt.Errorf("xwal: detach %q channel %q: %w", node, c.Name, err)
		}
	}
	x.Close()

	// Publish: .fork per channel, then .from LAST. That order is
	// load-bearing. A crash between two channels' .fork writes leaves each
	// channel individually correct, because .from still names the parent: a
	// flipped channel reads its own absorbed copy, an unflipped one still
	// delegates, and the two are byte-identical. Clearing .from first would
	// strand every channel not yet flipped, which is a hole.
	for _, dir := range dirs {
		if err := writeSyncedFile(filepath.Join(dir, ".fork"), []byte("base=1\n")); err != nil {
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

// absorbPrefix copies [1, base) out of the parent chain into dir as ONE
// segment based at 1, streaming straight from the read.
//
// One segment, not many: a reducible channel's segment header is the folded
// state at its start, and only the segment based at 1 can honestly carry the
// INITIAL state. Chunking would have to re-fold a watermark per chunk, and
// getting that subtly wrong reproduces the wrong state on every later read.
// Packing is segment normalization's job, and it is deferred.
func absorbPrefix(dir string, src *log.Log, base uint64, initial []byte, codec segment.SegmentCodec) error {
	if base <= 1 {
		return nil
	}
	// Build under a scratch name and rename into place. segment.Create is
	// O_EXCL, so a crash that left a partial file at the FINAL name would
	// make every retry fail with EEXIST -- and crash recovery here IS
	// re-running Detach, so that would leave the store unrecoverable by the
	// one mechanism meant to recover it.
	//
	// The name is pid-scoped, so no live writer can collide, and it is
	// removed first: a crash leaves OUR scratch behind, and O_EXCL would
	// then reject the retry for exactly the reason we are avoiding.
	tmpName := filepath.Join(dir, fmt.Sprintf(".absorb-%d", os.Getpid()))
	if err := os.Remove(tmpName); err != nil && !os.IsNotExist(err) {
		return err
	}
	defer os.Remove(tmpName)
	// maxSize 0: unbounded, so the whole prefix lands in this one segment.
	seg, err := segment.Create(tmpName, codec, 1, 0)
	if err != nil {
		return err
	}
	if initial != nil {
		if err := seg.WriteHeader(initial); err != nil {
			seg.Close()
			return err
		}
	}
	rerr := src.Range(1, func(idx uint64, payload []byte) error {
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
	if err := os.Rename(tmpName, filepath.Join(dir, segFileName(1, codec))); err != nil {
		return err
	}
	return disk.SyncDir(dir)
}

// DetachAll detaches every node in the set. Order does not matter: each
// absorbs from the chain as it stands when its turn comes, and an ancestor
// that has already detached still serves the same bytes.
func (t *Trunks) DetachAll(nodes []string) error {
	for _, n := range nodes {
		if err := t.Detach(n); err != nil {
			return err
		}
	}
	return nil
}
