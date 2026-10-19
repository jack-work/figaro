package xwal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jack-work/figaro/internal/store/disk"
)

type ComponentChannel = manifestChannel

type ComponentNode struct {
	Key   string `json:"key"`
	From  string `json:"from,omitempty"`
	Kind  string `json:"kind"`
	Trunk string `json:"trunk"`
}

type Component struct {
	Codec    string             `json:"codec"`
	Main     string             `json:"main"`
	Channels []ComponentChannel `json:"channels"`
	Nodes    []ComponentNode    `json:"nodes"`
}

func (c Component) TrunkIDs() []string {
	out := make([]string, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		out = append(out, n.Trunk)
	}
	return out
}

var ErrTrunkExists = errors.New("xwal: trunk already exists")

func isConversation(n *NodeInfo) bool {
	return n != nil && !n.IsRoot && n.Trunk != "" && (n.Kind == "" || n.Kind == "conversation")
}

func (t *Trunks) Component(trunk string) (Component, error) {
	end, err := t.beginFlatCreate()
	if err != nil {
		return Component{}, err
	}
	defer end()
	return t.componentLocked(trunk)
}

func (t *Trunks) componentLocked(trunk string) (Component, error) {
	key, ok := t.idx.Head(trunk)
	if !ok {
		return Component{}, fmt.Errorf("%w %q", ErrUnknownTrunk, trunk)
	}
	if !isConversation(t.node(key)) {
		return Component{}, fmt.Errorf("xwal: trunk %q is not a conversation", trunk)
	}
	top := key
	for seen := 0; seen <= t.idx.Len(); seen++ {
		n := t.node(top)
		if !isConversation(t.node(n.From)) {
			break
		}
		top = n.From
	}
	man, err := readManifestFile(t.root)
	if err != nil {
		return Component{}, err
	}
	c := Component{Codec: man.Codec, Main: man.Main, Channels: man.Channels}
	kids := t.idx.ChildIndex()
	queue := []string{top}
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		n := t.node(k)
		if !isConversation(n) {
			return Component{}, fmt.Errorf("xwal: node %q below %q is not a conversation", k, top)
		}
		from := n.From
		if k == top {
			from = ""
		}
		c.Nodes = append(c.Nodes, ComponentNode{Key: k, From: from, Kind: n.Kind, Trunk: n.Trunk})
		queue = append(queue, kids[k]...)
	}
	return c, nil
}

type ComponentFile struct {
	Node    string
	Channel string
	Name    string
	Size    int64
	path    string
}

func (f ComponentFile) Open() (*os.File, error) { return os.Open(f.path) }

type Export struct {
	Component
	Files   []ComponentFile
	scratch string
}

func (e *Export) Close() error {
	if e.scratch == "" {
		return nil
	}
	return os.RemoveAll(e.scratch)
}

func (t *Trunks) Export(trunk, token string) (*Export, error) {
	if !validToken(token) {
		return nil, fmt.Errorf("xwal: invalid move token %q", token)
	}
	end, err := t.beginFlatCreate()
	if err != nil {
		return nil, err
	}
	defer end()
	c, err := t.componentLocked(trunk)
	if err != nil {
		return nil, err
	}
	e := &Export{Component: c, scratch: filepath.Join(t.root, ".export-"+token)}
	if err := os.RemoveAll(e.scratch); err != nil {
		return nil, err
	}
	if err := t.exportTop(e); err != nil {
		e.Close()
		return nil, err
	}
	for i, n := range c.Nodes {
		for _, ch := range c.Channels {
			if i == 0 && e.absorbed(ch.Name) {
				if err := e.addDir(n.Key, ch.Name, filepath.Join(e.scratch, ch.Name), func(name string) bool { return name != nodeMarkerName }); err != nil {
					e.Close()
					return nil, err
				}
				if err := e.addDir(n.Key, ch.Name, filepath.Join(t.root, ch.Name, fsName(n.Key)), func(name string) bool {
					return name != nodeMarkerName && name != ".fork"
				}); err != nil {
					e.Close()
					return nil, err
				}
				continue
			}
			if err := e.addDir(n.Key, ch.Name, filepath.Join(t.root, ch.Name, fsName(n.Key)), func(name string) bool { return name != nodeMarkerName }); err != nil {
				e.Close()
				return nil, err
			}
		}
	}
	return e, nil
}

func (e *Export) absorbed(channel string) bool {
	_, err := os.Stat(filepath.Join(e.scratch, channel))
	return err == nil
}

func (t *Trunks) exportTop(e *Export) error {
	top := e.Nodes[0].Key
	x, err := t.openHotTopology(top)
	if err != nil {
		return err
	}
	defer x.Close()
	for _, c := range x.Channels() {
		ch := x.channel(c.Name)
		if _, err := os.Stat(filepath.Join(t.root, c.Name, fsName(top))); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		l, err := ch.Log()
		if err != nil {
			return err
		}
		base := l.ForkBase()
		if base <= 1 {
			continue
		}
		dir := filepath.Join(e.scratch, c.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		var initial []byte
		if ch.kind == ChannelReducible {
			initial = ch.initial
		}
		if err := absorbPrefix(dir, l, base, initial, x.codec); err != nil {
			return fmt.Errorf("xwal: export %q channel %q: %w", top, c.Name, err)
		}
		if err := writeSyncedFile(filepath.Join(dir, ".fork"), []byte("base=1\n")); err != nil {
			return err
		}
	}
	return nil
}

func (e *Export) addDir(node, channel, dir string, keep func(string) bool) error {
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, ent := range ents {
		name := ent.Name()
		if name == disk.ForkPendingName {
			return fmt.Errorf("xwal: node %q channel %q has a fork in progress", node, channel)
		}
		if !keep(name) {
			continue
		}
		if ent.IsDir() {
			return fmt.Errorf("xwal: node %q channel %q holds a directory %q", node, channel, name)
		}
		info, err := ent.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("xwal: node %q channel %q holds a non-regular file %q", node, channel, name)
		}
		e.Files = append(e.Files, ComponentFile{Node: node, Channel: channel, Name: name, Size: info.Size(), path: filepath.Join(dir, name)})
	}
	return nil
}

type Digest struct {
	lines []string
}

func (d *Digest) Add(node, channel, name string, size int64, sum [sha256.Size]byte) {
	d.lines = append(d.lines, fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%x", node, channel, name, size, sum))
}

func (d *Digest) Sum() string {
	sort.Strings(d.lines)
	h := sha256.New()
	for _, l := range d.lines {
		io.WriteString(h, l)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (e *Export) Digest() (string, error) {
	var d Digest
	for _, f := range e.Files {
		sum, size, err := hashFile(f.path)
		if err != nil {
			return "", err
		}
		d.Add(f.Node, f.Channel, f.Name, size, sum)
	}
	return d.Sum(), nil
}

func hashFile(path string) ([sha256.Size]byte, int64, error) {
	var sum [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return sum, 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return sum, 0, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, n, nil
}

var tokenPattern = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

func validToken(token string) bool { return tokenPattern.MatchString(token) }

const graftPrefix = ".graft-"
const graftPlanName = "plan.json"

type graftPlan struct {
	Component Component         `json:"component"`
	Keys      map[string]string `json:"keys,omitempty"`
}

type Graft struct {
	t    *Trunks
	dir  string
	plan graftPlan
}

func (t *Trunks) BeginGraft(token string, c Component) (*Graft, error) {
	if !validToken(token) {
		return nil, fmt.Errorf("xwal: invalid move token %q", token)
	}
	if err := t.admitComponent(c); err != nil {
		return nil, err
	}
	g := &Graft{t: t, dir: filepath.Join(t.root, graftPrefix+token), plan: graftPlan{Component: c}}
	if err := os.Mkdir(g.dir, 0o755); err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("xwal: move %s is already staged", token)
		}
		return nil, err
	}
	if err := g.writePlan(); err != nil {
		os.RemoveAll(g.dir)
		return nil, err
	}
	return g, nil
}

func (t *Trunks) OpenGraft(token string) (*Graft, error) {
	if !validToken(token) {
		return nil, fmt.Errorf("xwal: invalid move token %q", token)
	}
	g := &Graft{t: t, dir: filepath.Join(t.root, graftPrefix+token)}
	if err := g.readPlan(); err != nil {
		return nil, err
	}
	return g, nil
}

func (t *Trunks) admitComponent(c Component) error {
	man, err := readManifestFile(t.root)
	if err != nil {
		return err
	}
	if c.Codec != man.Codec {
		return fmt.Errorf("xwal: component codec %q, this store %q", c.Codec, man.Codec)
	}
	if c.Main != man.Main {
		return fmt.Errorf("xwal: component main channel %q, this store %q", c.Main, man.Main)
	}
	have := map[string]manifestChannel{}
	for _, mc := range man.Channels {
		have[mc.Name] = mc
	}
	seenCh := map[string]bool{}
	for _, mc := range c.Channels {
		if err := validChannelName(mc.Name); err != nil {
			return fmt.Errorf("xwal: component: %w", err)
		}
		if seenCh[mc.Name] {
			return fmt.Errorf("xwal: component names channel %q twice", mc.Name)
		}
		seenCh[mc.Name] = true
		if mine, ok := have[mc.Name]; ok && !t.sameChannel(mine, mc) {
			return fmt.Errorf("xwal: component channel %q is %+v, this store's is %+v", mc.Name, mc, mine)
		}
	}
	if !seenCh[c.Main] {
		return fmt.Errorf("xwal: component lacks its main channel %q", c.Main)
	}
	if len(c.Nodes) == 0 {
		return fmt.Errorf("xwal: empty component")
	}
	keys, trunks := map[string]bool{}, map[string]bool{}
	for i, n := range c.Nodes {
		if !validNodeKey(n.Key) {
			return fmt.Errorf("xwal: component node key %q", n.Key)
		}
		if keys[n.Key] {
			return fmt.Errorf("xwal: component names node %q twice", n.Key)
		}
		if n.Trunk == "" || trunks[n.Trunk] {
			return fmt.Errorf("xwal: component node %q has trunk %q", n.Key, n.Trunk)
		}
		if n.Kind != "conversation" {
			return fmt.Errorf("xwal: component node %q is %q, not a conversation", n.Key, n.Kind)
		}
		if (i == 0) != (n.From == "") || (i > 0 && !keys[n.From]) {
			return fmt.Errorf("xwal: component node %q comes from %q, which is not an earlier node", n.Key, n.From)
		}
		keys[n.Key], trunks[n.Trunk] = true, true
		if _, exists := t.idx.Head(n.Trunk); exists {
			return fmt.Errorf("%w %q", ErrTrunkExists, n.Trunk)
		}
	}
	return nil
}

func (t *Trunks) sameChannel(a, b manifestChannel) bool {
	if a.Kind != b.Kind || a.Opaque != b.Opaque || a.Unkeyed != b.Unkeyed {
		return false
	}
	return a.Reducer == b.Reducer || t.reducerKey(a) == t.reducerKey(b)
}

func (t *Trunks) reducerKey(c manifestChannel) string {
	if _, ok := t.cfg.Registry[c.Reducer]; ok {
		return c.Reducer
	}
	if _, ok := t.cfg.Registry[c.Name]; ok {
		return c.Name
	}
	if _, ok := builtinReducers()[c.Reducer]; ok {
		return "builtin:" + c.Reducer
	}
	return "unresolved:" + c.Reducer
}

func validNodeKey(k string) bool {
	return k != "" && k != "." && k != ".." && !strings.HasPrefix(k, ".") && !strings.ContainsAny(k, `/\`)
}

func (g *Graft) writePlan() error {
	body, err := json.Marshal(g.plan)
	if err != nil {
		return err
	}
	return writeSyncedFile(filepath.Join(g.dir, graftPlanName), body)
}

func (g *Graft) readPlan() error {
	body, err := os.ReadFile(filepath.Join(g.dir, graftPlanName))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, &g.plan)
}

func (g *Graft) Component() Component { return g.plan.Component }

func (g *Graft) sealed() bool { return g.plan.Keys != nil }

func (g *Graft) Write(node, channel, name string, offset int64, data []byte) error {
	if g.sealed() {
		return fmt.Errorf("xwal: move is committing; no further writes")
	}
	if !g.hasNode(node) {
		return fmt.Errorf("xwal: node %q is not in this move", node)
	}
	if !g.hasChannel(channel) {
		return fmt.Errorf("xwal: channel %q is not in this move", channel)
	}
	if name == nodeMarkerName || name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("xwal: file name %q", name)
	}
	dir := filepath.Join(g.dir, filepath.FromSlash(channel), node)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	switch {
	case info.Size() == offset+int64(len(data)):
		return nil
	case info.Size() != offset:
		return fmt.Errorf("xwal: %s/%s/%s is %d bytes, write at %d", channel, node, name, info.Size(), offset)
	}
	_, err = f.WriteAt(data, offset)
	return err
}

func (g *Graft) hasNode(key string) bool {
	for _, n := range g.plan.Component.Nodes {
		if n.Key == key {
			return true
		}
	}
	return false
}

func (g *Graft) hasChannel(name string) bool {
	for _, c := range g.plan.Component.Channels {
		if c.Name == name {
			return true
		}
	}
	return false
}

func (g *Graft) Digest() (string, error) {
	var d Digest
	for _, ch := range g.plan.Component.Channels {
		for _, n := range g.plan.Component.Nodes {
			dir := filepath.Join(g.dir, filepath.FromSlash(ch.Name), n.Key)
			ents, err := os.ReadDir(dir)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return "", err
			}
			for _, ent := range ents {
				sum, size, err := hashFile(filepath.Join(dir, ent.Name()))
				if err != nil {
					return "", err
				}
				d.Add(n.Key, ch.Name, ent.Name(), size, sum)
			}
		}
	}
	return d.Sum(), nil
}

func (g *Graft) Abort() error {
	if g.sealed() {
		return fmt.Errorf("xwal: move is committing; it cannot be aborted")
	}
	return os.RemoveAll(g.dir)
}

func (g *Graft) Commit(digest string) error {
	if !g.sealed() {
		got, err := g.Digest()
		if err != nil {
			return err
		}
		if got != digest {
			return fmt.Errorf("xwal: move digest %s, sender's %s", got, digest)
		}
		man, err := readManifestFile(g.t.root)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, mc := range man.Channels {
			have[mc.Name] = true
		}
		for _, mc := range g.plan.Component.Channels {
			if have[mc.Name] {
				continue
			}
			if err := g.t.ensureChannel(specOf(mc)); err != nil {
				return err
			}
		}
	}
	end, err := g.t.beginFlatCreate()
	if err != nil {
		return err
	}
	defer end()
	if !g.sealed() {
		if err := g.t.admitComponent(g.plan.Component); err != nil {
			return err
		}
		if err := g.mintKeys(); err != nil {
			return err
		}
		if err := g.writePlan(); err != nil {
			g.plan.Keys = nil
			return err
		}
	}
	if err := g.publish(); err != nil {
		return err
	}
	for _, n := range g.plan.Component.Nodes {
		g.t.idx.SpawnFlat(g.plan.Keys[n.From], g.plan.Keys[n.Key], n.Trunk, n.Kind)
	}
	return os.RemoveAll(g.dir)
}

func specOf(mc manifestChannel) ChannelSpec {
	kind := ChannelLog
	if mc.Kind == "reducible" {
		kind = ChannelReducible
	}
	return ChannelSpec{Name: mc.Name, Kind: kind, Reducer: mc.Reducer, Opaque: mc.Opaque, Unkeyed: mc.Unkeyed}
}

func (g *Graft) mintKeys() error {
	keys := map[string]string{"": ""}
	for _, n := range g.plan.Component.Nodes {
		for {
			k := g.t.mintNode()
			if !g.t.keyTaken(k) {
				keys[n.Key] = k
				break
			}
		}
	}
	g.plan.Keys = keys
	return nil
}

func (t *Trunks) keyTaken(key string) bool {
	names, err := channelNames(t.root)
	if err != nil {
		return true
	}
	for _, ch := range names {
		if pathExists(filepath.Join(t.root, ch, fsName(key))) {
			return true
		}
	}
	return false
}

func (g *Graft) publish() error {
	c := g.plan.Component
	touched := map[string]bool{}
	for _, n := range c.Nodes {
		dst := g.plan.Keys[n.Key]
		for _, ch := range c.Channels {
			if ch.Name == c.Main {
				continue
			}
			if err := g.place(ch.Name, n.Key, dst, touched); err != nil {
				return err
			}
		}
		staged := filepath.Join(g.dir, filepath.FromSlash(c.Main), n.Key)
		if pathExists(staged) || !pathExists(g.t.irDir(dst)) {
			if err := os.MkdirAll(staged, 0o755); err != nil {
				return err
			}
			if err := writeNodeMarker(staged, nodeMarker{from: g.plan.Keys[n.From], kind: n.Kind, trunk: n.Trunk}); err != nil {
				return err
			}
		}
		if err := g.place(c.Main, n.Key, dst, touched); err != nil {
			return err
		}
	}
	for dir := range touched {
		if err := disk.SyncDir(dir); err != nil {
			return err
		}
	}
	return nil
}

func (g *Graft) place(channel, src, dst string, touched map[string]bool) error {
	from := filepath.Join(g.dir, filepath.FromSlash(channel), src)
	to := filepath.Join(g.t.root, filepath.FromSlash(channel), fsName(dst))
	if !pathExists(from) {
		return nil
	}
	if pathExists(to) {
		return fmt.Errorf("xwal: move would overwrite %s", to)
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	touched[filepath.Dir(to)] = true
	touched[filepath.Dir(from)] = true
	return nil
}

func (t *Trunks) resumeGrafts() error {
	ents, err := os.ReadDir(t.root)
	if err != nil {
		return err
	}
	for _, ent := range ents {
		name := ent.Name()
		switch {
		case strings.HasPrefix(name, ".export-"):
			if err := os.RemoveAll(filepath.Join(t.root, name)); err != nil {
				return err
			}
		case strings.HasPrefix(name, removalPrefix):
			dir := filepath.Join(t.root, name)
			body, err := os.ReadFile(filepath.Join(dir, graftPlanName))
			if err != nil {
				if err := os.RemoveAll(dir); err != nil {
					return err
				}
				continue
			}
			var c Component
			if err := json.Unmarshal(body, &c); err != nil {
				return fmt.Errorf("xwal: removal plan %s: %w", name, err)
			}
			if err := t.removeNodes(c, dir); err != nil {
				return err
			}
		case strings.HasPrefix(name, graftPrefix):
			g := &Graft{t: t, dir: filepath.Join(t.root, name)}
			if err := g.readPlan(); err != nil || !g.sealed() {
				continue
			}
			if err := g.publish(); err != nil {
				return fmt.Errorf("xwal: resume move %s: %w", strings.TrimPrefix(name, graftPrefix), err)
			}
			if err := os.RemoveAll(g.dir); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *Trunks) PendingGrafts() []string {
	ents, _ := os.ReadDir(t.root)
	var out []string
	for _, ent := range ents {
		if strings.HasPrefix(ent.Name(), graftPrefix) {
			out = append(out, strings.TrimPrefix(ent.Name(), graftPrefix))
		}
	}
	return out
}

const removalPrefix = ".remove-"

func (t *Trunks) RemoveComponent(c Component, token string) ([]string, error) {
	if !validToken(token) {
		return nil, fmt.Errorf("xwal: invalid move token %q", token)
	}
	endMutation, err := t.beginTopologyMutation()
	if err != nil {
		return nil, err
	}
	defer endMutation()
	if err := t.ensureNoOpenHeads(); err != nil {
		return nil, err
	}
	t.retireRootHotPreservingValidation()
	inside := map[string]bool{}
	for _, n := range c.Nodes {
		inside[n.Key] = true
	}
	kids := t.idx.ChildIndex()
	for _, n := range c.Nodes {
		have := t.node(n.Key)
		if have == nil || have.Trunk != n.Trunk {
			return nil, fmt.Errorf("xwal: node %q no longer carries trunk %q", n.Key, n.Trunk)
		}
		for _, k := range kids[n.Key] {
			if !inside[k] {
				return nil, fmt.Errorf("xwal: node %q gained a child %q outside the component", n.Key, k)
			}
		}
	}
	dir := filepath.Join(t.root, removalPrefix+token)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	body, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	if err := writeSyncedFile(filepath.Join(dir, graftPlanName), body); err != nil {
		return nil, err
	}
	if err := t.removeNodes(c, dir); err != nil {
		return nil, err
	}
	return c.TrunkIDs(), t.rebuild()
}

func (t *Trunks) removeNodes(c Component, plan string) error {
	names, err := channelNames(t.root)
	if err != nil {
		return err
	}
	for i := len(c.Nodes) - 1; i >= 0; i-- {
		key := c.Nodes[i].Key
		if err := os.RemoveAll(filepath.Join(t.irDir(key), nodeMarkerName)); err != nil {
			return err
		}
	}
	for _, n := range c.Nodes {
		for _, ch := range names {
			if err := os.RemoveAll(filepath.Join(t.root, ch, fsName(n.Key))); err != nil {
				return err
			}
		}
	}
	if err := disk.SyncDir(t.irDir("")); err != nil {
		return err
	}
	return os.RemoveAll(plan)
}
