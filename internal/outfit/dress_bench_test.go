package outfit_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/outfit"
)

// The resolver's own instruments. What they measure is the ONE call the whole
// daemon makes now — Dress — on two shapes: the composition a real config
// carries, and an enormous generated tree that lives OUTSIDE this repository
// (Gluck, 2026-08-11: megabytes of generated TOML have no business in git).
//
//	~/notes/figaro/tests/huge-outfits/gen.py
//	FIGARO_BENCH_OUTFIT_DIR=<tree> go test ./internal/outfit -bench Dress -count=6
//
// Absent that variable the huge cases skip and the small ones still run, so
// the suite is honest on any machine.

const envHugeTree = "FIGARO_BENCH_OUTFIT_DIR"

// benchTree writes the composition a real config carries: a shared base under
// a diamond, plus a skills directory.
func benchTree(b *testing.B, skillCount, skillBytes int) string {
	b.Helper()
	dir := b.TempDir()
	skills := filepath.Join(dir, "skills")
	if err := os.MkdirAll(skills, 0o755); err != nil {
		b.Fatal(err)
	}
	body := strings.Repeat("lorem ipsum dolor sit amet, consectetur adipiscing elit. ", skillBytes/56+1)
	for i := 0; i < skillCount; i++ {
		text := fmt.Sprintf("---\nname: skill-%02d\ndescription: one of many\n---\n%s", i, body)
		if err := os.WriteFile(filepath.Join(skills, fmt.Sprintf("skill-%02d.md", i)), []byte(text), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	write := func(name, text string) {
		if err := os.MkdirAll(filepath.Join(dir, "outfits"), 0o700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "outfits", name+".toml"), []byte(text), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	write("house", "skills = { dirName = \"skills\" }\n[system]\ncache_control = \"5m\"\n")
	write("terse", "layers = [\"house\"]\n[system]\nverbosity = \"low\"\n")
	write("thorough", "layers = [\"house\"]\n[system]\nthinking_effort = \"high\"\n")
	write("full", "layers = [\"terse\", \"thorough\"]\n[system]\nprovider = \"anthropic\"\nmodel = \"claude-sonnet-4-5\"\n")
	return dir
}

func hugeTree(b *testing.B) string {
	b.Helper()
	dir := os.Getenv(envHugeTree)
	if dir == "" {
		b.Skipf("set %s to a generated tree (see ~/notes/figaro/tests/huge-outfits/gen.py)", envHugeTree)
	}
	if _, err := os.Stat(filepath.Join(dir, "outfits")); err != nil {
		b.Skipf("%s=%s has no outfits/ directory", envHugeTree, dir)
	}
	return dir
}

// BenchmarkDressWarm is the price every dressed call pays once the closure is
// cached: the fold, the copy, and nothing from disk but a stat per dependency.
func BenchmarkDressWarm(b *testing.B) {
	o := outfit.New(benchTree(b, 40, 4096))
	if _, err := o.Dress([]string{"full"}, form.Patch{}, ""); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := o.Dress([]string{"full"}, form.Patch{}, ""); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDressCold is the first ask after a daemon start: every file in the
// closure read and parsed.
func BenchmarkDressCold(b *testing.B) {
	dir := benchTree(b, 40, 4096)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := outfit.New(dir).Dress([]string{"full"}, form.Patch{}, ""); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDressNoOutfits is the cost of the check on a call that names none —
// every plain `fig set` pays this and no more. It must stay at zero-ish.
func BenchmarkDressNoOutfits(b *testing.B) {
	o := outfit.New(benchTree(b, 40, 4096))
	patch, err := outfit.ParseSet(`mantra="x"`)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := o.Dress(nil, patch, ""); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDressHugeWarm folds the enormous tree's root, warm. This is the
// number the caching exists for.
func BenchmarkDressHugeWarm(b *testing.B) {
	o := outfit.New(hugeTree(b))
	if _, err := o.Dress([]string{"full"}, form.Patch{}, ""); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := o.Dress([]string{"full"}, form.Patch{}, ""); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDressHugeCold folds it from nothing: hundreds of files, a DAG with
// heavy sharing, and the skills directory once.
func BenchmarkDressHugeCold(b *testing.B) {
	dir := hugeTree(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := outfit.New(dir).Dress([]string{"full"}, form.Patch{}, ""); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDressHugeCycle is the fault path, repeated: a name whose closure
// loops must be refused, and refusing it a second time must not cost a second
// walk.
func BenchmarkDressHugeCycle(b *testing.B) {
	o := outfit.New(hugeTree(b))
	if _, err := o.Dress([]string{"loop-a"}, form.Patch{}, ""); err == nil {
		b.Fatal("want a cycle error from loop-a")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := o.Dress([]string{"loop-a"}, form.Patch{}, ""); err == nil {
			b.Fatal("want a cycle error")
		}
	}
}
