package xwal

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/jack-work/figaro/figwal/disk"
	"github.com/jack-work/figaro/figwal/log"
	"github.com/jack-work/figaro/figwal/segment"
)

// ErrAbsorbConflict: a channel holds a segment where its absorbed prefix
// belongs, below its fork base, and the bytes are not that prefix. Nothing
// is replaced.
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
	unlockNode := lockAbsorb("detach\x00" + canonicalDir(t.irDir(node)))
	defer unlockNode()

	x, err := t.openHotTopology(node)
	if err != nil {
		return err
	}
	type pending struct {
		channel, dir string
		log          *log.Log
		base         uint64
		initial      []byte
	}
	var dirs []string
	var absorb []pending
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
			return fmt.Errorf("xwal: detach %q channel %q: %w", node, c.Name, lerr)
		}
		base := chLog.ForkBase()
		if base <= 1 {
			continue // already its own root in this channel
		}
		absorb = append(absorb, pending{c.Name, dir, chLog, base, ch.absorbInitial()})
	}
	keys := make([]string, 0, len(absorb))
	for _, p := range absorb {
		keys = append(keys, canonicalDir(p.dir))
	}
	slices.Sort(keys)
	for _, k := range keys {
		defer lockAbsorb(k)()
	}
	for _, p := range absorb {
		if err := absorbPrefix(p.dir, p.log, p.base, p.initial, x.codec); err != nil {
			x.Close()
			return fmt.Errorf("xwal: detach %q channel %q: %w", node, p.channel, err)
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

var absorbLocks = struct {
	sync.Mutex
	held map[string]*keyedLock
}{held: map[string]*keyedLock{}}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

// lockAbsorb serializes, within this process, everything that builds or
// judges an absorbed prefix under one key; the store's root lock keeps
// other processes out. Two Trunks on one root, or a Detach beside a live
// handle opening the same channel, otherwise race on one dir.
func lockAbsorb(key string) func() {
	absorbLocks.Lock()
	l := absorbLocks.held[key]
	if l == nil {
		l = &keyedLock{}
		absorbLocks.held[key] = l
	}
	l.refs++
	absorbLocks.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		absorbLocks.Lock()
		if l.refs--; l.refs == 0 {
			delete(absorbLocks.held, key)
		}
		absorbLocks.Unlock()
	}
}

func canonicalDir(dir string) string {
	if p, err := filepath.EvalSymlinks(dir); err == nil {
		return p
	}
	if p, err := filepath.Abs(dir); err == nil {
		return p
	}
	return filepath.Clean(dir)
}

// absorbPrefix never replaces a file: a segment already at the absorbed
// name is either this very prefix (a Detach re-run after a crash) or the
// node's own records, and only the first may stand.
func absorbPrefix(dir string, src *log.Log, base uint64, initial []byte, codec segment.SegmentCodec) error {
	if base <= 1 {
		return nil
	}
	tmpName, from, err := buildAbsorbed(dir, src, base, initial, codec)
	if err != nil {
		return err
	}
	defer os.Remove(tmpName)
	if from > 1 {
		if err := disk.StubHole(dir, segFileName(1, codec)); err != nil {
			return err
		}
	}
	name := segFileName(from, codec)
	if err := os.Link(tmpName, filepath.Join(dir, name)); errors.Is(err, fs.ErrExist) {
		same, cerr := sameFileBytes(tmpName, filepath.Join(dir, name))
		if cerr != nil {
			return cerr
		}
		if !same {
			return fmt.Errorf("%w: %s base %d segment %s", ErrAbsorbConflict, dir, base, name)
		}
	} else if err != nil {
		return err
	}
	return disk.SyncDir(dir)
}

// buildAbsorbed writes the segment that holds src's records [from, base)
// under a scratch name in dir that no other call shares, and returns that
// path and from. The caller removes the scratch.
func buildAbsorbed(dir string, src *log.Log, base uint64, initial []byte, codec segment.SegmentCodec) (path string, from uint64, err error) {
	from, header := uint64(1), initial
	if initial != nil && src.Floor() > 0 {
		h, segBase, err := src.SegmentHeaderAt(base - 1)
		if err != nil {
			return "", 0, err
		}
		from, header = segBase, h
	}
	f, err := os.CreateTemp(dir, ".absorb-*")
	if err != nil {
		return "", 0, err
	}
	path = f.Name()
	defer func() {
		if err != nil {
			os.Remove(path)
			path = ""
		}
	}()
	if err := f.Close(); err != nil {
		return "", 0, err
	}
	// maxSize 0: unbounded, so the whole prefix lands in this one segment.
	seg, err := segment.Open(path, codec, from, 0)
	if err != nil {
		return "", 0, err
	}
	if header != nil {
		if err := seg.WriteHeader(header); err != nil {
			seg.Close()
			return "", 0, err
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
		return "", 0, rerr
	}
	if err := seg.Sync(); err != nil {
		seg.Close()
		return "", 0, err
	}
	if err := seg.Close(); err != nil {
		return "", 0, err
	}
	return path, from, nil
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
	unlock := lockAbsorb(canonicalDir(dir))
	defer unlock()
	onDisk, err := readForkBaseFile(dir)
	if err != nil {
		return err
	}
	if onDisk <= 1 {
		// A Detach finished this channel while the log was loading.
		l.AdoptAbsorbedPrefix()
		return nil
	}
	if onDisk != base {
		return fmt.Errorf("%w: %s .fork says base %d, the open log %d", ErrAbsorbConflict, dir, onDisk, base)
	}
	own, err := segmentBases(dir, codec)
	if err != nil {
		return err
	}
	if len(own) == 0 {
		return fmt.Errorf("%w: %s base %d: only a hole below the base", ErrAbsorbConflict, dir, base)
	}
	first := own[0]
	tmp, from, err := buildAbsorbed(dir, l, base, initial, codec)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
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

func sameFileBytes(a, b string) (bool, error) {
	x, err := os.ReadFile(a)
	if err != nil {
		return false, err
	}
	y, err := os.ReadFile(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(x, y), nil
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
