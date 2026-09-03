// Command figaro-turnid-backfill stamps TurnID onto IR records written before
// TurnID existed.
//
// The derivation is turns.StampIDs, not a copy of it, so a backfilled log
// reads as the derive-on-read path made it read. Nodes are walked
// parents-first because a fork continues its parent's numbering: a child
// seeds from the counter its parent had reached at the fork base.
//
// Segments are rewritten through segment.JSONLCodec, so canonicalization and
// the _hash sidecar cannot drift from the reader.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store/segment"
	"github.com/jack-work/figaro/internal/turns"
)

const maxSegmentSize = 1 << 40 // no rotation while rewriting

type node struct {
	dir      string
	name     string
	parent   string
	forkBase uint64
	kind     string
	// counterAt[idx] is the turn counter AFTER the record at idx. A child
	// forking at base B seeds from counterAt[B-1].
	counterAt map[uint64]uint64
	final     uint64
	records   int
	stamped   int
}

func main() {
	var (
		root   = flag.String("store", "", "state dir (the one holding arias/)")
		apply  = flag.Bool("apply", false, "write the changes; default is a dry run")
		verify = flag.String("verify", "", "compare against the ORIGINAL store at this path: proves the backfill is a semantic no-op")
		vquiet = flag.Bool("quiet", false, "totals only")
	)
	flag.Parse()
	if *root == "" {
		die("--store is required")
	}
	irDir := filepath.Join(*root, "arias", "ir")
	if _, err := os.Stat(irDir); err != nil {
		die("no IR channel at %s: %v", irDir, err)
	}

	// A live daemon must not be rewritten under. Taking its own lock is both
	// the check and the mutual exclusion.
	if *apply {
		lock, err := holdStoreLock(*root)
		if err != nil {
			die("%v", err)
		}
		defer lock.Close()
	}

	nodes, err := loadNodes(irDir)
	if err != nil {
		die("%v", err)
	}
	order, err := topo(nodes)
	if err != nil {
		die("%v", err)
	}

	var totRec, totStamp, touchedNodes int
	for _, name := range order {
		n := nodes[name]
		seed := uint64(0)
		if n.parent != "" {
			if p, ok := nodes[n.parent]; ok {
				seed = p.counterAtOrBefore(n.forkBase)
			}
		}
		if err := n.derive(seed, *apply); err != nil {
			die("node %s: %v", n.name, err)
		}
		totRec += n.records
		totStamp += n.stamped
		if n.stamped > 0 {
			touchedNodes++
			if !*vquiet {
				fmt.Printf("  %-28s %5d records  %5d stamped  seed=%d final=%d\n",
					n.name, n.records, n.stamped, seed, n.final)
			}
		}
	}

	if *verify != "" {
		if err := runVerify(*verify, irDir); err != nil {
			die("VERIFY FAILED: %v", err)
		}
		fmt.Println("verify: every stored turn id equals what derive-on-read produced, and every already-stamped record is byte-identical")
		return
	}

	verb := "would stamp"
	if *apply {
		verb = "stamped"
	}
	fmt.Printf("\n%d records across %d nodes; %s %d on %d nodes\n",
		totRec, len(order), verb, totStamp, touchedNodes)
	if !*apply {
		fmt.Println("dry run: nothing written. re-run with --apply")
	}
}

// counterAtOrBefore is the counter as of the last record at or below idx-1:
// what a child forking at idx inherits.
func (n *node) counterAtOrBefore(base uint64) uint64 {
	if base == 0 {
		return 0
	}
	best := uint64(0)
	var bestIdx uint64
	found := false
	for idx, c := range n.counterAt {
		if idx < base && (!found || idx > bestIdx) {
			best, bestIdx, found = c, idx, true
		}
	}
	return best
}

// derive walks this node's own records in index order, applying StampIDs'
// rule, and rewrites the segments that changed.
func (n *node) derive(seed uint64, apply bool) error {
	segs, err := segmentsOf(n.dir)
	if err != nil {
		return err
	}
	n.counterAt = map[uint64]uint64{}
	cur := seed

	for _, sg := range segs {
		payloads, err := readSegment(sg)
		if err != nil {
			return fmt.Errorf("read %s: %w", sg.path, err)
		}
		changed := false
		out := make([][]byte, 0, len(payloads))

		for i, raw := range payloads {
			idx := sg.base + uint64(i)
			n.records++

			var frame map[string]json.RawMessage
			if err := json.Unmarshal(raw, &frame); err != nil {
				// Not a frame we understand: pass it through untouched.
				out = append(out, raw)
				n.counterAt[idx] = cur
				continue
			}
			mraw, ok := frame["p"]
			if !ok || len(mraw) == 0 || string(mraw) == "null" {
				out = append(out, raw)
				n.counterAt[idx] = cur
				continue
			}
			var msg message.Message
			if err := json.Unmarshal(mraw, &msg); err != nil {
				out = append(out, raw)
				n.counterAt[idx] = cur
				continue
			}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(mraw, &obj); err != nil {
				return fmt.Errorf("idx %d: message is not an object: %w", idx, err)
			}
			_, stampedAlready := obj["turn_id"]

			// turns.StampIDs, exactly: a zero does not reset the counter.
			if turns.Opens(msg) {
				cur++
			}
			if msg.TurnID != 0 {
				cur = msg.TurnID
			}

			// KEY PRESENCE, NOT VALUE. An explicit zero is a stamped record
			// that belongs to no turn; an absent field is an unstamped one.
			// Reading the value cannot tell them apart, so counting by it
			// reports every pre-prompt record as outstanding forever.
			if stampedAlready {
				out = append(out, raw)
				n.counterAt[idx] = cur
				continue
			}
			obj["turn_id"] = json.RawMessage(strconv.FormatUint(cur, 10))
			newMsg, err := json.Marshal(obj)
			if err != nil {
				return fmt.Errorf("idx %d: remarshal message: %w", idx, err)
			}
			frame["p"] = newMsg
			newFrame, err := json.Marshal(frame)
			if err != nil {
				return fmt.Errorf("idx %d: remarshal frame: %w", idx, err)
			}
			out = append(out, newFrame)
			changed = true
			n.stamped++
			n.counterAt[idx] = cur
		}

		if changed && apply {
			if err := rewriteSegment(sg, out); err != nil {
				return fmt.Errorf("rewrite %s: %w", sg.path, err)
			}
		}
	}
	n.final = cur
	return nil
}

type segFile struct {
	path string
	base uint64
}

func segmentsOf(dir string) ([]segFile, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []segFile
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		base, err := strconv.ParseUint(strings.TrimSuffix(e.Name(), ".jsonl"), 10, 64)
		if err != nil {
			continue
		}
		out = append(out, segFile{path: filepath.Join(dir, e.Name()), base: base})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].base < out[j].base })
	return out, nil
}

func readSegment(sg segFile) ([][]byte, error) {
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

// rewriteSegment writes to a temp segment and renames over the original. The
// codec re-frames each record, so _idx and _hash are correct by construction.
func rewriteSegment(sg segFile, payloads [][]byte) error {
	tmp := sg.path + ".backfill"
	os.Remove(tmp)
	s, err := segment.Create(tmp, segment.JSONLCodec{}, sg.base, maxSegmentSize)
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

func loadNodes(irDir string) (map[string]*node, error) {
	ents, err := os.ReadDir(irDir)
	if err != nil {
		return nil, err
	}
	out := map[string]*node{}
	// The genesis channel lives at the root, in no node directory. It adds 0
	// to the numbering, but it is a record and the invariant covers records.
	if segs, err := segmentsOf(irDir); err == nil && len(segs) > 0 {
		out["<genesis>"] = &node{dir: irDir, name: "<genesis>", kind: "genesis"}
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(irDir, e.Name())
		n := &node{dir: dir, name: e.Name()}
		for k, v := range readKV(filepath.Join(dir, ".node")) {
			switch k {
			case "from":
				n.parent = v
			case "kind":
				n.kind = v
			}
		}
		for k, v := range readKV(filepath.Join(dir, ".fork")) {
			if k == "base" {
				n.forkBase, _ = strconv.ParseUint(v, 10, 64)
			}
		}
		out[e.Name()] = n
	}
	return out, nil
}

func readKV(path string) map[string]string {
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

// topo orders nodes parents-first. A parent named but absent is treated as a
// root: the store has nodes whose ancestor was collected.
func topo(nodes map[string]*node) ([]string, error) {
	state := map[string]int{} // 0 unvisited, 1 visiting, 2 done
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

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "turnid-backfill: "+f+"\n", a...)
	os.Exit(1)
}

// runVerify derives turn ids from the original store the way the reader does
// and asserts the backfilled store stored the same ones, on every record. It
// also asserts a record that already had an id came through byte for byte.
//
// A backfill that renumbers history would be silent: `send <id>:<turn>` would
// address the wrong exchange forever.
func runVerify(origRoot, newIR string) error {
	origIR := filepath.Join(origRoot, "arias", "ir")
	orig, err := loadNodes(origIR)
	if err != nil {
		return err
	}
	order, err := topo(orig)
	if err != nil {
		return err
	}

	var checked, identical int
	for _, name := range order {
		on := orig[name]
		seed := uint64(0)
		if on.parent != "" {
			if p, ok := orig[on.parent]; ok {
				seed = p.counterAtOrBefore(on.forkBase)
			}
		}
		if err := on.derive(seed, false); err != nil {
			return fmt.Errorf("derive %s: %w", name, err)
		}

		newDir := filepath.Join(newIR, name)
		if name == "<genesis>" {
			newDir = newIR
		}
		segs, err := segmentsOf(newDir)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		origSegs, err := segmentsOf(on.dir)
		if err != nil {
			return fmt.Errorf("%s (orig): %w", name, err)
		}
		if len(segs) != len(origSegs) {
			return fmt.Errorf("%s: segment count %d != %d", name, len(segs), len(origSegs))
		}

		cur := seed
		for si, sg := range segs {
			newPayloads, err := readSegment(sg)
			if err != nil {
				return fmt.Errorf("%s: read new: %w", name, err)
			}
			oldPayloads, err := readSegment(origSegs[si])
			if err != nil {
				return fmt.Errorf("%s: read orig: %w", name, err)
			}
			if len(newPayloads) != len(oldPayloads) {
				return fmt.Errorf("%s: record count %d != %d", name, len(newPayloads), len(oldPayloads))
			}
			for i := range newPayloads {
				idx := sg.base + uint64(i)
				oldMsg, okOld := messageOf(oldPayloads[i])
				newMsg, okNew := messageOf(newPayloads[i])
				if okOld != okNew {
					return fmt.Errorf("%s idx %d: record shape changed", name, idx)
				}
				if !okOld {
					continue
				}
				// What the reader WOULD have derived, from the original.
				if turns.Opens(oldMsg) {
					cur++
				}
				want := cur
				if oldMsg.TurnID != 0 {
					want = oldMsg.TurnID
					cur = oldMsg.TurnID
				}
				if newMsg.TurnID != want {
					return fmt.Errorf("%s idx %d: stored turn %d, derive-on-read says %d",
						name, idx, newMsg.TurnID, want)
				}
				checked++
				if hadTurnKey(oldPayloads[i]) {
					if string(oldPayloads[i]) != string(newPayloads[i]) {
						return fmt.Errorf("%s idx %d: already had an id and was rewritten anyway", name, idx)
					}
					identical++
				}
			}
		}
	}
	fmt.Printf("verify: %d records checked, %d already-stamped records byte-identical\n", checked, identical)
	return nil
}

func messageOf(raw []byte) (message.Message, bool) {
	var frame map[string]json.RawMessage
	if json.Unmarshal(raw, &frame) != nil {
		return message.Message{}, false
	}
	mraw, ok := frame["p"]
	if !ok || len(mraw) == 0 || string(mraw) == "null" {
		return message.Message{}, false
	}
	var m message.Message
	if json.Unmarshal(mraw, &m) != nil {
		return message.Message{}, false
	}
	return m, true
}

// holdStoreLock takes the daemon's own lock for the duration of the rewrite.
// Failure means a daemon owns this store and the answer is `figaro stop`, not
// a flag to force past it.
func holdStoreLock(stateDir string) (*os.File, error) {
	path := filepath.Join(stateDir, ".daemon.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("a daemon is running on %s: stop it first\n"+
			"  figaro stop --keep-pids     (or: FIGARO_STATE_DIR=%s figaro stop)", stateDir, stateDir)
	}
	return f, nil
}

// hadTurnKey reports whether the record carries the turn_id FIELD, which is
// the only way to distinguish a stamped zero from an unstamped record.
func hadTurnKey(raw []byte) bool {
	var frame map[string]json.RawMessage
	if json.Unmarshal(raw, &frame) != nil {
		return false
	}
	mraw, ok := frame["p"]
	if !ok {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(mraw, &obj) != nil {
		return false
	}
	_, ok = obj["turn_id"]
	return ok
}
