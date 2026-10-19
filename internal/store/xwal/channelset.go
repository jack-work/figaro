package xwal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/store/disk"
	"github.com/jack-work/figaro/internal/store/log"
	"github.com/jack-work/figaro/internal/store/segment"
)

type channelMap struct {
	byName map[string]*channel
	order  []string
}

type channelSet struct {
	mu  sync.Mutex
	gen atomic.Uint64
	cur atomic.Pointer[channelMap]
}

func newChannelSet(gen uint64, set *channelMap) *channelSet {
	cs := &channelSet{}
	cs.gen.Store(gen)
	cs.cur.Store(set)
	return cs
}

func (x *XWAL) channels() *channelMap {
	if x.cfg.channelGen != nil && x.cfg.channelGen() != x.cs.gen.Load() {
		x.adopt()
	}
	return x.cs.cur.Load()
}

func (x *XWAL) channel(name string) *channel { return x.channels().byName[name] }

func (x *XWAL) adopt() {
	gen := x.cfg.channelGen()
	x.cs.mu.Lock()
	defer x.cs.mu.Unlock()
	if gen == x.cs.gen.Load() {
		return
	}
	man, err := readManifestFile(x.root)
	if err != nil {
		return
	}
	for _, mc := range man.Channels {
		_ = x.publishLocked(mc)
	}
	x.cs.gen.Store(gen)
}

func (x *XWAL) publish(mc manifestChannel) error {
	x.cs.mu.Lock()
	defer x.cs.mu.Unlock()
	return x.publishLocked(mc)
}

func (x *XWAL) publishLocked(mc manifestChannel) error {
	cur := x.cs.cur.Load()
	if cur.byName[mc.Name] != nil {
		return nil
	}
	ch, err := x.newChannel(mc)
	if err != nil {
		return err
	}
	next := &channelMap{byName: make(map[string]*channel, len(cur.byName)+1), order: append(append([]string(nil), cur.order...), mc.Name)}
	for k, v := range cur.byName {
		next.byName[k] = v
	}
	next.byName[mc.Name] = ch
	x.cs.cur.Store(next)
	return nil
}

func (x *XWAL) newChannel(mc manifestChannel) (*channel, error) {
	ch := &channel{name: mc.Name, rname: mc.Reducer, opaque: mc.Opaque, unkeyed: mc.Unkeyed}
	switch mc.Kind {
	case "reducible":
		ch.kind = ChannelReducible
		r, ok := resolveReducer(x.cfg, mc.Reducer, mc.Name)
		if !ok || r.Reduce == nil {
			return nil, fmt.Errorf("xwal: no reducer %q registered for channel %q", mc.Reducer, mc.Name)
		}
		ch.reduce = r.Reduce
		ch.initial = r.Initial
	default:
		ch.kind = ChannelLog
	}
	opts := x.channelOpts(ch)
	store := x.store
	name := mc.Name
	if x.lazyNode(name) {
		node := x.branch[0]
		cdir := filepath.Join(x.root, name, fsName(node))
		ch.dir = cdir
		ch.opener = func() (*log.Log, error) {
			opts := opts
			if pathExists(cdir) {
				p, err := x.openFlatParent(name, x.cfg.ParentOf(node), opts, store)
				if err != nil {
					return nil, err
				}
				if p != nil {
					opts.Parent = p.Disk()
				}
			} else {
				opts.MayBeAbsent = true
			}
			return openLog(store, name, cdir, opts)
		}
		ch.materialize = func() error { return materializeNode(cdir, ch, x.codec) }
	} else {
		cdir := x.channelDir(name)
		ch.dir = cdir
		branch := x.branch
		ch.opener = func() (*log.Log, error) {
			opts := opts
			if x.cfg.ParentOf != nil && len(branch) == 1 {
				p, err := x.openFlatParent(name, x.cfg.ParentOf(branch[0]), opts, store)
				if err != nil {
					return nil, err
				}
				if p != nil {
					opts.Parent = p.Disk()
				}
			}
			return openLog(store, name, cdir, opts)
		}
	}
	ch.fk = map[uint64]uint64{}
	return ch, nil
}

func (x *XWAL) lazyNode(name string) bool {
	return name != x.main && x.cfg.ParentOf != nil && len(x.branch) == 1
}

func openLog(store *log.Store, name, dir string, opts disk.Options) (*log.Log, error) {
	var l *log.Log
	var err error
	if store == nil {
		l, err = log.Open(dir, opts)
	} else {
		l, err = store.Open(dir, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("xwal: open channel %q: %w", name, err)
	}
	return l, nil
}

func materializeNode(dir string, ch *channel, codec segment.SegmentCodec) error {
	if err := mkdirAllSynced(dir); err != nil {
		return err
	}
	marker := filepath.Join(dir, ".fork")
	if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
		if err := writeSyncedFile(marker, []byte("base=1\n")); err != nil {
			return err
		}
		if err := disk.SyncDir(dir); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if ch.kind != ChannelReducible {
		return nil
	}
	return ensureWatermark(dir, 1, codec, ch.initial)
}

func (ch *channel) writable() (*log.Log, error) {
	l, err := ch.Log()
	if err != nil || !l.Absent() {
		return l, err
	}
	if ch.materialize == nil {
		return nil, fmt.Errorf("xwal: channel %q has no directory here", ch.name)
	}
	if err := ch.materialize(); err != nil {
		return nil, err
	}
	return ch.Log()
}

func registerChannel(root string, cfg Config, spec ChannelSpec) error {
	canon, err := canonicalRoot(root)
	if err != nil {
		return err
	}
	state := rootTopologyStateFor(canon)
	state.manifestMu.Lock()
	defer state.manifestMu.Unlock()
	man, err := readManifestFile(root)
	if err != nil {
		return err
	}
	if err := validateChannelSpec(root, cfg, man, spec); err != nil {
		return err
	}
	for _, mc := range man.Channels {
		if mc.Name == spec.Name {
			return nil
		}
	}
	if err := rootChannel(root, cfg, man, spec.manifest()); err != nil {
		return err
	}
	man.Channels = append(man.Channels, spec.manifest())
	if err := writeManifest(root, man); err != nil {
		return err
	}
	state.channelGen.Add(1)
	return nil
}

func rootChannel(root string, cfg Config, man manifest, mc manifestChannel) error {
	dir := filepath.Join(root, mc.Name)
	if err := mkdirAllSynced(dir); err != nil {
		return err
	}
	if mc.Kind != ChannelReducible.String() {
		return nil
	}
	ch, err := channelFromManifest(cfg, mc)
	if err != nil {
		return err
	}
	codec, err := codecByName(man.Codec)
	if err != nil {
		return err
	}
	return ensureWatermark(dir, 1, codec, ch.initial)
}

func channelGenFor(root string) func() uint64 {
	state := rootTopologyStateFor(root)
	return state.channelGen.Load
}
