package formapply_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/formapply"
)

// The golden was written from the hand-kept lists as they stood at 5f83ebbac
// (v0.74.2 plus managed fan-out). Each line is a path and every verdict a
// writer class decides for it; the registry must reproduce all of them.

var updateWritersGolden = flag.Bool("update-writers-golden", false, "rewrite testdata/key_writers.golden from the current tree")

const writersGoldenPath = "testdata/key_writers.golden"

func writerVerdicts(path string) string {
	owner, _ := form.OwnerHeld(path)
	lineage, _ := form.Protected(path)
	set := form.CheckWritable(form.Build(form.Snapshot{}, map[string]json.RawMessage{path: json.RawMessage(`1`)}, nil), false) != nil
	rm := form.CheckWritable(form.Build(form.Snapshot{}, nil, []string{path}), false) != nil
	managed := func(operator []string) string {
		if _, err := formapply.Plan(form.Snapshot{}, form.Snapshot{}, []string{path}, form.ProtectedKeys(), operator); err != nil {
			return "refused"
		}
		return "ok"
	}
	return fmt.Sprintf("owner=%s lineage=%s set=%v remove=%v apply.owner=%s apply.figaro=%s",
		owner, lineage, set, rm, managed(formapply.OperatorPrefixes), managed(nil))
}

func writersGoldenProbes() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	var keys []string
	for _, k := range form.WellKnownKeys() {
		keys = append(keys, strings.ReplaceAll(k.Key, "<name>", "x"))
	}
	keys = append(keys, form.OwnerHeldKeys()...)
	keys = append(keys, form.ProtectedKeys()...)
	keys = append(keys, form.SystemManagedKeys()...)
	keys = append(keys, formapply.OperatorPrefixes...)
	for _, k := range keys {
		add(k)
		add(k + ".x")
		add(k + "[0]")
		add(k + "_x")
		if i := strings.LastIndex(k, "."); i > 0 {
			add(k[:i])
		}
	}
	segs := []string{"system", "fanout", "max_children", "mint", "quota", "scopes", "grants", "sandbox", "retention", "mode", "methods", "environment", "decide", "base_url", "checkpoint", "marks", "failure", "reason", "retired", "credo", "model", "mantra", "x", "turn", "runtime"}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 400; i++ {
		n := 1 + r.Intn(4)
		parts := make([]string, n)
		for j := range parts {
			parts[j] = segs[r.Intn(len(segs))]
		}
		add(strings.Join(parts, "."))
	}
	sort.Strings(out)
	return out
}

func TestTheRegistryReproducesTheHandKeptLists(t *testing.T) {
	if *updateWritersGolden {
		var b strings.Builder
		for _, p := range writersGoldenProbes() {
			b.WriteString(p + "\t" + writerVerdicts(p) + "\n")
		}
		if err := os.WriteFile(writersGoldenPath, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(writersGoldenPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) < 500 {
		t.Fatalf("golden holds %d paths; it should hold every known key and the fuzzed set", len(lines))
	}
	for _, line := range lines {
		path, want, _ := strings.Cut(line, "\t")
		if got := writerVerdicts(path); got != want {
			t.Errorf("%s:\n got  %s\n want %s", path, got, want)
		}
	}
}
