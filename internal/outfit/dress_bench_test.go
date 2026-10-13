package outfit_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/outfit"
)

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

func BenchmarkDressCold(b *testing.B) {
	dir := benchTree(b, 40, 4096)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := outfit.New(dir).Dress([]string{"full"}, form.Patch{}, ""); err != nil {
			b.Fatal(err)
		}
	}
}

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
