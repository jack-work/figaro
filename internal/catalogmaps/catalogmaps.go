package catalogmaps

import (
	"embed"
	"fmt"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/internal/catalog"
)

//go:embed *.toml
var files embed.FS

func Builtin(name string) catalog.Mapping {
	m, err := load(name)
	if err != nil {
		panic(err)
	}
	return m
}

func load(name string) (catalog.Mapping, error) {
	data, err := files.ReadFile(name + ".toml")
	if err != nil {
		return catalog.Mapping{}, fmt.Errorf("catalogmaps: no built-in mapping %q", name)
	}
	var f struct {
		Catalog catalog.Mapping `toml:"catalog"`
	}
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return catalog.Mapping{}, fmt.Errorf("catalogmaps: %s: %w", name, err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		return catalog.Mapping{}, fmt.Errorf("catalogmaps: %s: unknown keys %v", name, extra)
	}
	if _, err := catalog.New(f.Catalog, nil); err != nil {
		return catalog.Mapping{}, fmt.Errorf("catalogmaps: %s: %w", name, err)
	}
	return f.Catalog, nil
}
