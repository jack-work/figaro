package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store/segment"
	"github.com/jack-work/figaro/internal/turns"
)

// The migrations that rewrite records in place, and the walk they share.
//
// Each runs with the store CLOSED, is idempotent, and rewrites through
// segment.JSONLCodec so the frame's index and content hash are the codec's own
// rather than arithmetic here.

const migrateSegmentSize = 1 << 40

// migrateNode is one channel directory: its records, and where it forked from.
type migrateNode struct {
	dir    string
	name   string
	parent string
	seen   bool
	// carry is whatever a migration needs to hand a child, since a fork
	// continues its parent's state.
	carry any
}

// rewriteChannel walks every node of a channel, parents first, handing each
// record to convert. A nil result leaves the record alone.
//
// Parents first is not a detail: a fork inherits its parent's state, so a
// migration that needs one -- the board a patch lands on, the turn a log has
// reached -- must have visited the parent already.
func rewriteChannel(root, channel string, seed func() any,
	convert func(n *migrateNode, payload []byte) ([]byte, error)) error {

	dir := filepath.Join(root, channel)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	nodes, order, err := loadMigrateNodes(dir)
	if err != nil {
		return err
	}
	for _, name := range order {
		n := nodes[name]
		n.carry = seed()
		if p, ok := nodes[n.parent]; ok && p.seen {
			n.carry = p.carry
		}
		segs, err := migrateSegments(n.dir)
		if err != nil {
			return err
		}
		for _, sg := range segs {
			payloads, err := readMigrateSegment(sg)
			if err != nil {
				return fmt.Errorf("read %s: %w", sg.path, err)
			}
			out := make([][]byte, 0, len(payloads))
			changed := false
			for _, raw := range payloads {
				next, err := convert(n, raw)
				if err != nil {
					return fmt.Errorf("%s: %w", sg.path, err)
				}
				if next == nil {
					out = append(out, raw)
					continue
				}
				out = append(out, next)
				changed = true
			}
			if changed {
				if err := rewriteMigrateSegment(sg, out); err != nil {
					return fmt.Errorf("rewrite %s: %w", sg.path, err)
				}
			}
		}
		n.seen = true
	}
	return nil
}

// MigrateFormToStructural is the form channel v1 -> v2: flat patches become
// structural ones.
//
// It replays each node in order, so at every record it holds the board that
// record applies to. That is the whole reason this is a migration and not a
// decoder: a flat patch says "set these dotted keys" and records nothing of
// what it replaced, and only a caller holding the prior board can turn that
// into a patch carrying what it destroyed. The result inverts; a decoded one
// never could.
func MigrateFormToStructural(root string) error {
	return rewriteChannel(root, chanForm,
		func() any { return form.Snapshot{} },
		func(n *migrateNode, raw []byte) ([]byte, error) {
			frame, praw, ok := framePayload(raw)
			if !ok {
				return nil, nil
			}
			board, _ := n.carry.(form.Snapshot)

			flat, isFlat := readFlatPatch(praw)
			if !isFlat {
				var p message.Patch
				if json.Unmarshal(praw, &p) == nil {
					n.carry = board.Apply(p)
				}
				return nil, nil
			}

			p := form.Build(board, flat.Set, flat.Remove)
			n.carry = board.Apply(p)

			nb, err := json.Marshal(p)
			if err != nil {
				return nil, fmt.Errorf("marshal patch: %w", err)
			}
			frame["p"] = nb
			return json.Marshal(frame)
		})
}

// MigrateStampTurnIDs is the fig IR channel v4 -> v5: every record carries
// turn_id explicitly.
//
// The rule is turns.StampIDs', so a stamped log reads as the derive-on-read
// path made it read. A record belonging to no turn keeps 0 and now says so,
// which is the distinction the field's absence could not make.
func MigrateStampTurnIDs(root string) error {
	return rewriteChannel(root, chanIR,
		func() any { return uint64(0) },
		func(n *migrateNode, raw []byte) ([]byte, error) {
			frame, praw, ok := framePayload(raw)
			if !ok {
				return nil, nil
			}
			var msg message.Message
			if json.Unmarshal(praw, &msg) != nil {
				return nil, nil
			}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(praw, &obj); err != nil {
				return nil, nil
			}
			_, stamped := obj["turn_id"]

			cur, _ := n.carry.(uint64)
			if turns.Opens(msg) {
				cur++
			}
			if msg.TurnID != 0 {
				cur = msg.TurnID
			}
			n.carry = cur

			// Key presence, not value: an explicit zero is a stamped record
			// that belongs to no turn, and reading the value cannot tell it
			// from one that was never written.
			if stamped {
				return nil, nil
			}
			obj["turn_id"] = json.RawMessage(strconv.FormatUint(cur, 10))
			nb, err := json.Marshal(obj)
			if err != nil {
				return nil, err
			}
			frame["p"] = nb
			return json.Marshal(frame)
		})
}

// ---- shared plumbing ------------------------------------------------------

func framePayload(raw []byte) (map[string]json.RawMessage, json.RawMessage, bool) {
	var frame map[string]json.RawMessage
	if json.Unmarshal(raw, &frame) != nil {
		return nil, nil, false
	}
	p, ok := frame["p"]
	if !ok || len(p) == 0 || string(p) == "null" {
		return nil, nil, false
	}
	return frame, p, true
}

type flatPatch struct {
	Set    map[string]json.RawMessage `json:"set"`
	Remove []string                   `json:"remove"`
}

func readFlatPatch(raw json.RawMessage) (flatPatch, bool) {
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return flatPatch{}, false
	}
	_, hasSet := probe["set"]
	_, hasRemove := probe["remove"]
	if !hasSet && !hasRemove {
		return flatPatch{}, false
	}
	var out flatPatch
	if json.Unmarshal(raw, &out) != nil {
		return flatPatch{}, false
	}
	return out, true
}

type migrateSegFile struct {
	path string
	base uint64
}

func migrateSegments(dir string) ([]migrateSegFile, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []migrateSegFile
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		base, err := strconv.ParseUint(strings.TrimSuffix(e.Name(), ".jsonl"), 10, 64)
		if err != nil {
			continue
		}
		out = append(out, migrateSegFile{path: filepath.Join(dir, e.Name()), base: base})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].base < out[j].base })
	return out, nil
}

func readMigrateSegment(sg migrateSegFile) ([][]byte, error) {
	s, err := segment.OpenReadOnly(sg.path, segment.JSONLCodec{}, sg.base)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	n := s.Count()
	out := make([][]byte, 0, n)
	for i := uint64(0); i < n; i++ {
		p, err := s.ReadIndex(i)
		if err != nil {
			return nil, fmt.Errorf("index %d: %w", i, err)
		}
		out = append(out, append([]byte(nil), p...))
	}
	return out, nil
}

func rewriteMigrateSegment(sg migrateSegFile, payloads [][]byte) error {
	tmp := sg.path + ".migrate"
	os.Remove(tmp)
	s, err := segment.Create(tmp, segment.JSONLCodec{}, sg.base, migrateSegmentSize)
	if err != nil {
		return err
	}
	for _, p := range payloads {
		if _, err := s.Append(p); err != nil {
			s.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := s.Sync(); err != nil {
		s.Close()
		os.Remove(tmp)
		return err
	}
	if err := s.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, sg.path)
}

func loadMigrateNodes(dir string) (map[string]*migrateNode, []string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	out := map[string]*migrateNode{}
	if segs, err := migrateSegments(dir); err == nil && len(segs) > 0 {
		out["<root>"] = &migrateNode{dir: dir, name: "<root>"}
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		nd := filepath.Join(dir, e.Name())
		n := &migrateNode{dir: nd, name: e.Name()}
		// The main channel owns lineage for every channel. Related channel
		// directories have a .fork but no .node, so reading identity there
		// would convert every child against an empty board.
		marker := filepath.Join(filepath.Dir(dir), chanIR, e.Name(), ".node")
		n.parent = readMigrateKV(marker)["from"]
		if n.parent == "" {
			n.parent = "<root>"
		}
		// A detached channel already contains its prefix, even if detach
		// stopped before publishing the main channel's new identity.
		if readMigrateKV(filepath.Join(nd, ".fork"))["base"] == "1" {
			n.parent = ""
		}
		out[e.Name()] = n
	}
	order, err := migrateTopo(out)
	return out, order, err
}

func readMigrateKV(path string) map[string]string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			m[k] = v
		}
	}
	return m
}

func migrateTopo(nodes map[string]*migrateNode) ([]string, error) {
	state := map[string]int{}
	var order []string
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("lineage cycle at %s", name)
		}
		state[name] = 1
		if n, ok := nodes[name]; ok && n.parent != "" {
			if _, ok := nodes[n.parent]; ok {
				if err := visit(n.parent); err != nil {
					return err
				}
			}
		}
		state[name] = 2
		order = append(order, name)
		return nil
	}
	names := make([]string, 0, len(nodes))
	for k := range nodes {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := visit(n); err != nil {
			return nil, err
		}
	}
	return order, nil
}
