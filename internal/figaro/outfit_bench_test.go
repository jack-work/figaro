package figaro_test

import (
	"fmt"
	"github.com/jack-work/figaro/api/message"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/store"
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

func benchAgent(b *testing.B, configDir string, initial form.Patch) *figaro.Agent {
	b.Helper()
	cb, err := form.Open(filepath.Join(b.TempDir(), "the form channel"))
	if err != nil {
		b.Fatal(err)
	}
	if !initial.IsEmpty() {
		cb.Apply(initial)
	}
	testBE, testID := store.NewTestAria(b, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:    testBE,
		Projector:  uiir.New(nil),
		ID:         testID,
		SocketPath: filepath.Join(b.TempDir(), "sock"),
		Provider:   &formSpyProvider{},
		Tools:      tool.NewRegistry(),
		Form:       cb,
	})
	b.Cleanup(func() { a.Kill() })
	return a
}

// BenchmarkDressFirstTime is materializing a composition onto a FRESH board:
// the full cost: the closure fold plus the patch append.
func BenchmarkDressFirstTime(b *testing.B) {
	dir := benchConfig(b, 40, 4096)
	a := benchAgent(b, dir, form.Patch{})
	patch := benchDress(b, dir, "full")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := a.Set(patch, 0); err != nil {
			b.Fatal(err)
		}
	}
}

// benchDress is what the API boundary hands the agent now: MATERIALIZED keys.
// The fold that produced them is benchmarked where it lives, in internal/outfit
// (BenchmarkDressWarm / BenchmarkDressCold): the agent's price is the append.
func benchDress(b *testing.B, configDir string, names ...string) form.Patch {
	patch, err := outfit.New(configDir).Dress(names, form.Patch{}, "")
	if err != nil {
		b.Fatal(err)
	}
	return patch
}
