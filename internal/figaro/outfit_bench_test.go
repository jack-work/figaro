package figaro_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// benchConfig writes a composition of the size a real config carries: a shared
// base under a diamond, plus a skills directory.
func benchConfig(b *testing.B, skillCount, skillBytes int) string {
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

func benchAgent(b *testing.B, configDir string, initial chalkboard.Patch) *figaro.Agent {
	b.Helper()
	cb, err := chalkboard.Open(filepath.Join(b.TempDir(), "chalkboard.json"))
	if err != nil {
		b.Fatal(err)
	}
	if !initial.IsEmpty() {
		cb.Apply(initial)
	}
	a := figaro.NewAgent(figaro.Config{
		Projector:  uiir.New(nil),
		ID:         "outfit-bench",
		SocketPath: filepath.Join(b.TempDir(), "sock"),
		Provider:   &chalkSpyProvider{},
		Outfitter:  outfit.New(configDir),
		Tools:      tool.NewRegistry(),
		Chalkboard: cb,
	})
	b.Cleanup(func() { a.Kill() })
	return a
}

// BenchmarkApplyOutfitFirstTime is applying to a FRESH board: every key is new,
// so this is the full cost — fold plus the additive diff plus the patch append.
func BenchmarkApplyOutfitFirstTime(b *testing.B) {
	dir := benchConfig(b, 40, 3000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		a := benchAgent(b, dir, chalkboard.Patch{})
		b.StartTimer()
		if _, err := a.ApplyOutfit(outfit.Names("full")); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkApplyOutfitReapplied is applying to a board that ALREADY matches:
// the fold is cached and the additive diff comes out empty, so nothing is
// written. This is the common case — re-applying an outfit to a live aria.
//
// The board is seeded directly rather than by a first ApplyOutfit, because
// Agent.Set is asynchronous: it queues the patch for the agent's run loop, so
// an immediate second call would still read the pre-apply snapshot.
func BenchmarkApplyOutfitReapplied(b *testing.B) {
	dir := benchConfig(b, 40, 3000)
	seed, err := outfit.New(dir).Load("full")
	if err != nil {
		b.Fatal(err)
	}
	a := benchAgent(b, dir, chalkboard.Patch{Set: seed.Set})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		set, err := a.ApplyOutfit(outfit.Names("full"))
		if err != nil {
			b.Fatal(err)
		}
		if len(set) != 0 {
			b.Fatalf("re-apply wrote %d keys; the additive diff should be empty", len(set))
		}
	}
}
