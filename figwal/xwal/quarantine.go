package xwal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/jack-work/figaro/figwal/disk"
	"github.com/jack-work/figaro/figwal/segment"
)

type Quarantined struct {
	Trunk   string
	Node    string
	Channel string
	Segment string
	Offset  int64
	Shared  []string
}

type QuarantineRelease struct {
	Quarantined
	BytesCut      int64
	LaterSegments []string
	Aside         string
	Trimmed       map[string]int
	Applied       bool
}

var ErrNotQuarantined = errors.New("xwal: no quarantined segment matches")

func (t *Trunks) channelDir(channel, key string) string {
	if channel == t.main {
		return t.irDir(key)
	}
	return filepath.Join(t.root, channel, fsName(key))
}

func (t *Trunks) ScanQuarantine() ([]Quarantined, error) {
	man, err := readManifestFile(t.root)
	if err != nil {
		return nil, err
	}
	codec, err := codecByName(man.Codec)
	if err != nil {
		return nil, err
	}
	t.mu.RLock()
	nodes := t.idx.All()
	t.mu.RUnlock()
	keys := make([]string, 0, len(nodes))
	for k := range nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Quarantined
	for _, key := range keys {
		for _, mc := range man.Channels {
			dir := t.channelDir(mc.Name, key)
			if !pathExists(dir) {
				continue
			}
			bases, err := segmentBases(dir, codec)
			if err != nil {
				return nil, err
			}
			for i, base := range bases {
				path := filepath.Join(dir, segFileName(base, codec))
				at, bad, err := corruptAt(path, codec, i < len(bases)-1)
				if err != nil {
					return nil, err
				}
				if !bad {
					continue
				}
				out = append(out, Quarantined{
					Trunk: nodes[key].Trunk, Node: key, Channel: mc.Name,
					Segment: path, Offset: at, Shared: t.descendantTrunks(key, nodes[key].Trunk),
				})
			}
		}
	}
	return out, nil
}

func (t *Trunks) descendantTrunks(key, own string) []string {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(k string) {
		for _, child := range t.idx.ChildrenOf(k) {
			if n, ok := t.idx.Node(child); ok && n.Trunk != "" && n.Trunk != own {
				seen[n.Trunk] = true
			}
			walk(child)
		}
	}
	walk(key)
	out := make([]string, 0, len(seen))
	for tr := range seen {
		out = append(out, tr)
	}
	sort.Strings(out)
	return out
}

func (t *Trunks) ReleaseQuarantine(trunk, channel string, offset int64, asideDir string, apply bool) (QuarantineRelease, error) {
	all, err := t.ScanQuarantine()
	if err != nil {
		return QuarantineRelease{}, err
	}
	t.mu.RLock()
	leaf, ok := t.idx.Head(trunk)
	t.mu.RUnlock()
	if !ok {
		return QuarantineRelease{}, fmt.Errorf("xwal: no figaro %q", trunk)
	}
	var mine, elsewhere []Quarantined
	for _, q := range all {
		if q.Node == leaf {
			mine = append(mine, q)
		} else if q.Trunk == trunk || hasKey(t.lineageKeys(leaf), q.Node) {
			elsewhere = append(elsewhere, q)
		}
	}
	var hit []Quarantined
	for _, q := range mine {
		if q.Offset == offset && (channel == "" || q.Channel == channel) {
			hit = append(hit, q)
		}
	}
	switch {
	case len(hit) == 1 && len(hit[0].Shared) > 0:
		return QuarantineRelease{}, fmt.Errorf("xwal: %s's corrupt segment %s is history it shares with %s; cutting it would cut theirs too, so it is not released here",
			trunk, hit[0].Segment, strings.Join(hit[0].Shared, ", "))
	case len(hit) == 0 && len(elsewhere) > 0:
		return QuarantineRelease{}, fmt.Errorf("xwal: %s's corrupt segment %s is in node %s, which it shares with %s; cutting it would cut their histories too, so it is not released here",
			trunk, elsewhere[0].Segment, elsewhere[0].Node, strings.Join(append([]string{elsewhere[0].Trunk}, elsewhere[0].Shared...), ", "))
	case len(hit) == 0:
		return QuarantineRelease{}, fmt.Errorf("%w: %s has no corrupt frame at byte %d%s", ErrNotQuarantined, trunk, offset, describe(mine))
	case len(hit) > 1:
		return QuarantineRelease{}, fmt.Errorf("xwal: %s has corrupt frames at byte %d in several channels (%s); name one with --channel", trunk, offset, describe(hit))
	}
	q := hit[0]
	man, err := readManifestFile(t.root)
	if err != nil {
		return QuarantineRelease{}, err
	}
	codec, err := codecByName(man.Codec)
	if err != nil {
		return QuarantineRelease{}, err
	}
	headered := false
	for _, mc := range man.Channels {
		if mc.Name == q.Channel && mc.Kind == ChannelReducible.String() {
			headered = true
		}
	}
	dir := filepath.Dir(q.Segment)
	if headered && (q.Offset == 0 || q.Offset < firstFrameEnd(q.Segment, codec)) {
		return QuarantineRelease{}, fmt.Errorf("xwal: the corrupt frame in %s is the segment's watermark header; releasing it needs the segment rebuilt, which this does not do", q.Segment)
	}
	fi, err := os.Stat(q.Segment)
	if err != nil {
		return QuarantineRelease{}, err
	}
	rel := QuarantineRelease{Quarantined: q, BytesCut: fi.Size() - q.Offset, Trimmed: map[string]int{}}
	bases, err := segmentBases(dir, codec)
	if err != nil {
		return rel, err
	}
	bad := strings.TrimSuffix(filepath.Base(q.Segment), codec.FileExt())
	for _, b := range bases {
		if p := filepath.Join(dir, segFileName(b, codec)); filepath.Base(p) > filepath.Base(q.Segment) {
			rel.LaterSegments = append(rel.LaterSegments, p)
		}
	}
	rel.Aside = filepath.Join(asideDir, fmt.Sprintf("%s-%s-%s", trunk, strings.ReplaceAll(q.Channel, "/", "_"), bad))
	if !apply {
		return rel, nil
	}

	if done, err := t.evictLineage(trunk); err != nil {
		return rel, err
	} else if !done {
		return rel, fmt.Errorf("xwal: %s is in use; release it when nothing holds it open", trunk)
	}
	if err := t.cutQuarantined(&rel, man, codec, leaf); err != nil {
		return rel, err
	}
	_, _ = t.evictLineage(trunk)
	rel.Applied = true
	return rel, nil
}

func (t *Trunks) cutQuarantined(rel *QuarantineRelease, man manifest, codec segment.SegmentCodec, leaf string) error {
	q := rel.Quarantined
	dir := filepath.Dir(q.Segment)
	unlock := t.lockLineage(q.Trunk)
	defer unlock()

	if err := os.MkdirAll(rel.Aside, 0o700); err != nil {
		return err
	}
	for _, p := range append([]string{q.Segment}, rel.LaterSegments...) {
		if err := copySynced(p, filepath.Join(rel.Aside, filepath.Base(p))); err != nil {
			return fmt.Errorf("xwal: copy %s aside: %w", p, err)
		}
	}
	if err := disk.SyncDir(rel.Aside); err != nil {
		return err
	}
	for i := len(rel.LaterSegments) - 1; i >= 0; i-- {
		if err := os.Remove(rel.LaterSegments[i]); err != nil {
			return err
		}
	}
	if err := truncateFileSynced(q.Segment, q.Offset); err != nil {
		return err
	}
	if err := disk.SyncDir(dir); err != nil {
		return err
	}
	if q.Channel == t.main {
		mainTail, err := nodeMainTail(t.irDir(leaf), codec)
		if err != nil {
			return err
		}
		for _, mc := range man.Channels {
			if mc.Name == man.Main {
				continue
			}
			cdir := t.channelDir(mc.Name, leaf)
			if !pathExists(cdir) {
				continue
			}
			n, err := trimChannelNode(cdir, codec, mc.Kind == ChannelReducible.String(), mainTail)
			if err != nil {
				return fmt.Errorf("xwal: %s was cut at byte %d (the originals are in %s), but trimming %s back to it failed: %w; restore from that copy or retry the trim",
					q.Segment, q.Offset, rel.Aside, mc.Name, err)
			}
			if n > 0 {
				rel.Trimmed[mc.Name] = n
			}
		}
	}
	if h := t.hot; h != nil && h.store != nil {
		for _, mc := range man.Channels {
			_ = h.store.Evict(t.channelDir(mc.Name, leaf))
		}
	}
	return nil
}

func (t *Trunks) lineageKeys(leaf string) []string {
	var keys []string
	for k := leaf; k != ""; {
		keys = append(keys, k)
		n, ok := t.idx.Node(k)
		if !ok {
			break
		}
		k = n.From
	}
	return keys
}

func hasKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

func describe(qs []Quarantined) string {
	if len(qs) == 0 {
		return ""
	}
	parts := make([]string, len(qs))
	for i, q := range qs {
		parts[i] = fmt.Sprintf("%s at byte %d of %s", q.Channel, q.Offset, q.Segment)
	}
	return "; its corrupt frames: " + strings.Join(parts, ", ")
}

func copySynced(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func firstFrameEnd(path string, codec segment.SegmentCodec) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	end := int64(0)
	_ = codec.ScanFrames(f, func(off int64, frameLen int) error {
		end = off + int64(frameLen)
		return io.EOF
	})
	return end
}

var verifiedSegments sync.Map

func (t *Trunks) CheckLineage(trunk, channel string) error {
	t.mu.RLock()
	leaf, ok := t.idx.Head(trunk)
	t.mu.RUnlock()
	if !ok {
		return nil
	}
	man, err := readManifestFile(t.root)
	if err != nil {
		return err
	}
	codec, err := codecByName(man.Codec)
	if err != nil {
		return err
	}
	for _, key := range t.lineageKeys(leaf) {
		dir := t.channelDir(channel, key)
		if !pathExists(dir) {
			continue
		}
		bases, err := segmentBases(dir, codec)
		if err != nil {
			return err
		}
		for i, base := range bases {
			path := filepath.Join(dir, segFileName(base, codec))
			fi, err := os.Stat(path)
			if err != nil {
				return err
			}
			stamp := [2]int64{fi.Size(), fi.ModTime().UnixNano()}
			if seen, ok := verifiedSegments.Load(path); ok && seen.([2]int64) == stamp {
				continue
			}
			at, bad, err := corruptAt(path, codec, i < len(bases)-1)
			if err != nil {
				return err
			}
			if bad {
				return fmt.Errorf("segment %s: %w", path, &segment.CorruptFrameError{Offset: at})
			}
			verifiedSegments.Store(path, stamp)
		}
	}
	return nil
}

func corruptAt(path string, codec segment.SegmentCodec, sealed bool) (int64, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	end := int64(0)
	serr := codec.ScanFrames(f, func(off int64, frameLen int) error {
		end = off + int64(frameLen)
		return nil
	})
	var cf *segment.CorruptFrameError
	if errors.As(serr, &cf) {
		return cf.Offset, true, nil
	}
	if serr != nil {
		return 0, false, serr
	}
	if !sealed {
		return 0, false, nil
	}
	fi, err := f.Stat()
	if err != nil {
		return 0, false, err
	}
	return end, end < fi.Size(), nil
}
