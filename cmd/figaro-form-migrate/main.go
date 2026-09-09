// Command figaro-form-migrate rewrites the form channel from flat patches to
// structural ones.
//
// It replays each node's history in order, so at every record it holds the
// board that record applies to. That is what the runtime converter cannot do
// and why this is not the same operation: a flat patch says "set these dotted
// keys" with no record of what it replaced, and only a caller holding the
// prior board can turn that into a patch that carries what it destroyed.
//
// So a migrated history is INVERTIBLE where a converted one is not, and a key
// that is a prefix of another lands where the board says it should.
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

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/store/segment"
)

const maxSegmentSize = 1 << 40

type node struct {
	dir      string
	name     string
	parent   string
	forkBase uint64
	records  int
	rewrote  int
	// board is this node's state after its own records, so a child forking
	// from it starts where its parent stood.
	board form.Snapshot
	seen  bool
}

func main() {
	var (
		root   = flag.String("store", "", "state dir (the one holding arias/)")
		apply  = flag.Bool("apply", false, "write the changes; default is a dry run")
		verify = flag.String("verify", "", "compare boards against the ORIGINAL store at this path")
		quiet  = flag.Bool("quiet", false, "totals only")
	)
	flag.Parse()
	if *root == "" {
		die("--store is required")
	}
	formDir := filepath.Join(*root, "arias", "form")
	if _, err := os.Stat(formDir); err != nil {
		die("no form channel at %s: %v", formDir, err)
	}

	if *verify != "" {
		if err := runVerify(*verify, *root); err != nil {
			die("VERIFY FAILED: %v", err)
		}
		fmt.Println("verify: every board reduces to what it reduced to before")
		return
	}

	if *apply {
		lock, err := holdStoreLock(*root)
		if err != nil {
			die("%v", err)
		}
		defer lock.Close()
	}

	nodes, order, err := load(formDir)
	if err != nil {
		die("%v", err)
	}

	var totRec, totRewrote int
	for _, name := range order {
		n := nodes[name]
		if err := n.migrate(nodes, *apply); err != nil {
			die("node %s: %v", name, err)
		}
		totRec += n.records
		totRewrote += n.rewrote
		if n.rewrote > 0 && !*quiet {
			fmt.Printf("  %-24s %5d records  %5d rewritten\n", n.name, n.records, n.rewrote)
		}
	}

	verb := "would rewrite"
	if *apply {
		verb = "rewrote"
	}
	fmt.Printf("\n%d records across %d nodes; %s %d\n", totRec, len(order), verb, totRewrote)
	if !*apply {
		fmt.Println("dry run: nothing written. re-run with --apply")
	}
}

// migrate replays this node's records, converting each flat patch against the
// board it applies to.
func (n *node) migrate(all map[string]*node, apply bool) error {
	n.board = form.Snapshot{}
	if p, ok := all[n.parent]; ok && p.seen {
		n.board = p.board
	}

	segs, err := segmentsOf(n.dir)
	if err != nil {
		return err
	}
	for _, sg := range segs {
		payloads, err := readSegment(sg)
		if err != nil {
			return fmt.Errorf("read %s: %w", sg.path, err)
		}
		out := make([][]byte, 0, len(payloads))
		changed := false

		for _, raw := range payloads {
			n.records++
			var frame map[string]json.RawMessage
			if json.Unmarshal(raw, &frame) != nil {
				out = append(out, raw)
				continue
			}
			praw, ok := frame["p"]
			if !ok || len(praw) == 0 || string(praw) == "null" {
				out = append(out, raw)
				continue
			}
			flat, isFlat := readFlat(praw)
			if !isFlat {
				// Already structural: apply it and move on.
				var p form.Patch
				if json.Unmarshal(praw, &p) == nil {
					n.board = n.board.Apply(p)
				}
				out = append(out, raw)
				continue
			}

			// The conversion, against the board this record lands on.
			p := form.Build(n.board, flat.Set, flat.Remove)
			n.board = n.board.Apply(p)

			nb, err := json.Marshal(p)
			if err != nil {
				return fmt.Errorf("marshal patch: %w", err)
			}
			frame["p"] = nb
			nf, err := json.Marshal(frame)
			if err != nil {
				return fmt.Errorf("marshal frame: %w", err)
			}
			out = append(out, nf)
			changed = true
			n.rewrote++
		}

		if changed && apply {
			if err := rewriteSegment(sg, out); err != nil {
				return fmt.Errorf("rewrite %s: %w", sg.path, err)
			}
		}
	}
	n.seen = true
	return nil
}

type flatPatch struct {
	Set    map[string]json.RawMessage `json:"set"`
	Remove []string                   `json:"remove"`
}

func readFlat(raw json.RawMessage) (flatPatch, bool) {
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

// runVerify replays BOTH stores and compares the board each node reduces to.
// A migration that changes a board is not a migration.
func runVerify(origRoot, newRoot string) error {
	origNodes, origOrder, err := load(filepath.Join(origRoot, "arias", "form"))
	if err != nil {
		return err
	}
	newNodes, _, err := load(filepath.Join(newRoot, "arias", "form"))
	if err != nil {
		return err
	}
	var checked int
	for _, name := range origOrder {
		on := origNodes[name]
		if err := on.migrate(origNodes, false); err != nil {
			return fmt.Errorf("replay original %s: %w", name, err)
		}
		nn, ok := newNodes[name]
		if !ok {
			return fmt.Errorf("%s is missing from the migrated store", name)
		}
		if err := nn.migrate(newNodes, false); err != nil {
			return fmt.Errorf("replay migrated %s: %w", name, err)
		}
		want, err := json.Marshal(on.board)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		got, err := json.Marshal(nn.board)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if string(want) != string(got) {
			return fmt.Errorf("%s reduces differently\n  was: %s\n  now: %s", name, want, got)
		}
		checked++
	}
	fmt.Printf("verify: %d nodes replayed on both sides\n", checked)
	return nil
}

// ---- store plumbing -------------------------------------------------------

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

func rewriteSegment(sg segFile, payloads [][]byte) error {
	tmp := sg.path + ".migrate"
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

func load(formDir string) (map[string]*node, []string, error) {
	ents, err := os.ReadDir(formDir)
	if err != nil {
		return nil, nil, err
	}
	out := map[string]*node{}
	if segs, err := segmentsOf(formDir); err == nil && len(segs) > 0 {
		out["<root>"] = &node{dir: formDir, name: "<root>"}
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(formDir, e.Name())
		n := &node{dir: dir, name: e.Name()}
		for k, v := range readKV(filepath.Join(dir, ".node")) {
			if k == "from" {
				n.parent = v
			}
		}
		for k, v := range readKV(filepath.Join(dir, ".fork")) {
			if k == "base" {
				n.forkBase, _ = strconv.ParseUint(v, 10, 64)
			}
		}
		out[e.Name()] = n
	}
	order, err := topo(out)
	return out, order, err
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

func topo(nodes map[string]*node) ([]string, error) {
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

func holdStoreLock(stateDir string) (*os.File, error) {
	path := filepath.Join(stateDir, ".daemon.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("a daemon is running on %s: stop it first", stateDir)
	}
	return f, nil
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "form-migrate: "+f+"\n", a...)
	os.Exit(1)
}
