package xwal

import (
	"bytes"
	"context"
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

var errDetachStale = errors.New("xwal: the node changed between its copy and the publish")

// Detach makes a node self-sufficient: it absorbs the history prefix it
// currently reads through an ancestor, and stops pointing at one.
func (t *Trunks) Detach(node string) error {
	return t.DetachContext(context.Background(), node)
}

func (t *Trunks) DetachContext(ctx context.Context, node string) error {
	if node == "" {
		return fmt.Errorf("xwal: cannot detach the root")
	}
	unlockNode := lockAbsorb("detach\x00" + canonicalDir(t.irDir(node)))
	defer unlockNode()

	plan, err := t.planDetach(ctx, node)
	if err != nil {
		return err
	}
	defer plan.discard()
	if t.testDetachPlanned != nil {
		t.testDetachPlanned(node)
	}
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return err
	}
	defer endMutation()
	return t.publishDetach(node, plan)
}

type absorbedCopy struct {
	channel, dir string
	from, base   uint64
	scratch      string
}

type detachPlan struct {
	codec  segment.SegmentCodec
	dirs   []string
	copies []absorbedCopy
}

func (t *Trunks) planDetach(ctx context.Context, node string) (*detachPlan, error) {
	if t.node(node) == nil {
		return nil, fmt.Errorf("%w: no node %q", ErrUnknownTrunk, node)
	}
	plan := &detachPlan{}
	x, err := t.openHotTopology(node)
	if err != nil {
		return nil, err
	}
	defer x.Close()
	plan.codec = x.codec
	for _, c := range x.Channels() {
		ch := x.channel(c.Name)
		dir := filepath.Join(t.root, c.Name, fsName(node))
		if _, serr := os.Stat(dir); os.IsNotExist(serr) {
			continue
		} else if serr != nil {
			plan.discard()
			return nil, serr
		}
		chLog, lerr := ch.Log()
		if lerr != nil {
			plan.discard()
			return nil, fmt.Errorf("xwal: detach %q channel %q: %w", node, c.Name, lerr)
		}
		base := chLog.ForkBase()
		plan.dirs = append(plan.dirs, dir)
		if base <= 1 {
			continue
		}
		scratch, from, berr := buildAbsorbed(ctx, dir, chLog, base, ch.absorbInitial(), x.codec)
		if berr != nil {
			plan.discard()
			return nil, fmt.Errorf("xwal: detach %q channel %q: %w", node, c.Name, berr)
		}
		plan.copies = append(plan.copies, absorbedCopy{channel: c.Name, dir: dir, from: from, base: base, scratch: scratch})
	}
	return plan, nil
}

func (p *detachPlan) discard() {
	for _, c := range p.copies {
		os.Remove(c.scratch)
	}
}

func (t *Trunks) publishDetach(node string, plan *detachPlan) error {
	n := t.node(node)
	if n == nil || t.isDead([]string{node}) {
		return fmt.Errorf("%w: no node %q", ErrUnknownTrunk, node)
	}
	keys := make([]string, 0, len(plan.copies))
	for _, c := range plan.copies {
		keys = append(keys, canonicalDir(c.dir))
	}
	slices.Sort(keys)
	for _, k := range keys {
		defer lockAbsorb(k)()
	}
	for _, c := range plan.copies {
		base, err := readForkBaseFile(c.dir)
		if err != nil {
			return fmt.Errorf("xwal: detach %q channel %q: %w", node, c.channel, err)
		}
		if base != c.base {
			return fmt.Errorf("%w: detach %q channel %q copied below base %d, the channel now forks at %d", errDetachStale, node, c.channel, c.base, base)
		}
	}
	for _, c := range plan.copies {
		if err := linkAbsorbed(c.dir, c.scratch, c.from, c.base, plan.codec); err != nil {
			return fmt.Errorf("xwal: detach %q channel %q: %w", node, c.channel, err)
		}
	}
	for _, dir := range plan.dirs {
		if err := writeForkMarker(dir, 1); err != nil {
			return err
		}
	}
	kind := n.Kind
	if kind == "" {
		kind = "conversation"
	}
	if err := t.syncForkCaches(node); err != nil {
		return err
	}
	if err := writeNodeMarker(t.irDir(node), nodeMarker{kind: kind, trunk: n.Trunk}); err != nil {
		return err
	}
	return t.reshape(func() error { t.idx.Reparent(node, ""); return nil })
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
	tmpName, from, err := buildAbsorbed(context.Background(), dir, src, base, initial, codec)
	if err != nil {
		return err
	}
	defer os.Remove(tmpName)
	return linkAbsorbed(dir, tmpName, from, base, codec)
}

func linkAbsorbed(dir, tmpName string, from, base uint64, codec segment.SegmentCodec) error {
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
func buildAbsorbed(ctx context.Context, dir string, src *log.Log, base uint64, initial []byte, codec segment.SegmentCodec) (path string, from uint64, err error) {
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
	scratch := f.Name()
	defer func() {
		if err != nil {
			os.Remove(scratch)
		}
	}()
	if err := f.Close(); err != nil {
		return "", 0, err
	}
	// maxSize 0: unbounded, so the whole prefix lands in this one segment.
	seg, err := segment.Open(scratch, codec, from, 0)
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
		if err := ctx.Err(); err != nil {
			return err
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
	return scratch, from, nil
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
	tmp, from, err := buildAbsorbed(context.Background(), dir, l, base, initial, codec)
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
