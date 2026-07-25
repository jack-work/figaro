package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jack-work/figwal/xwal"
)

// schemaFileName is figaro's channel-schema sidecar, kept next to figwal's
// xwal.json rather than inside it. It cannot live in the manifest: figwal
// owns that file, and its manifestChannel struct is {name,kind,reducer,opaque}
// re-marshalled on every channel add — any field we wrote there would be
// silently dropped on the next rewrite.
const schemaFileName = "schema.json"

// schemaClass decides what a version bump costs.
type schemaClass uint8

const (
	classCanonical schemaClass = iota // the record itself; never cleared
	classDerived                      // a cache; clear it, regenerate lazily
	classReducible                    // watermark + patches; needs a converter
)

type channelSchema struct {
	version int
	class   schemaClass
}

// channelSchemas is what THIS binary understands. A key ending in "/" matches
// by prefix, so one entry covers translations-v2/anthropic and its siblings.
// Bump a version when a channel's on-disk payload shape changes.
var channelSchemas = map[string]channelSchema{
	chanIR:             {version: 1, class: classCanonical},
	chanChalkboard:     {version: 1, class: classReducible},
	"translations-v2/": {version: 1, class: classDerived},
	chanUI:             {version: 1, class: classDerived},
}

// schemaFor resolves a concrete channel name to its schema, returning the
// registry key that matched (the key, not the name, is what the sidecar
// records, so prefix families version as a unit).
func schemaFor(name string) (channelSchema, string, bool) {
	if s, ok := channelSchemas[name]; ok {
		return s, name, true
	}
	for key, s := range channelSchemas {
		if strings.HasSuffix(key, "/") && strings.HasPrefix(name, key) {
			return s, key, true
		}
	}
	return channelSchema{}, "", false
}

type schemaFile struct {
	Channels map[string]int `json:"channels"`
}

func readSchema(root string) (map[string]int, error) {
	raw, err := os.ReadFile(filepath.Join(root, schemaFileName))
	if os.IsNotExist(err) {
		return map[string]int{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f schemaFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("store schema: %w", err)
	}
	if f.Channels == nil {
		f.Channels = map[string]int{}
	}
	return f.Channels, nil
}

func writeSchema(root string, m map[string]int) error {
	raw, err := json.MarshalIndent(schemaFile{Channels: m}, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(root, schemaFileName+".tmp")
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(root, schemaFileName))
}

// ensureSchema gates opening the store on the on-disk channel schemas.
//
// Forward incompatibility is a hard stop: a store written by a NEWER figaro is
// refused rather than silently misread — the one failure mode nothing covered
// before. Backward migration is by class: derived caches are cleared here
// (rm -rf, cheap) and regenerate lazily on next use; the canonical record is
// never cleared, only ever derived-on-read; a reducible channel needs a real
// converter and fails loudly until one is registered.
func ensureSchema(root string, trunks *xwal.Store) error {
	stored, err := readSchema(root)
	if err != nil {
		return err
	}
	var bust []string
	for key, want := range channelSchemas {
		have, seen := stored[key]
		switch {
		case !seen, have == want.version:
		case have > want.version:
			return fmt.Errorf(
				"store channel %q is schema v%d but this figaro understands v%d: "+
					"refusing to open a store written by a newer build (upgrade figaro)",
				strings.TrimSuffix(key, "/"), have, want.version)
		case want.class == classDerived:
			bust = append(bust, key)
		case want.class == classCanonical:
			// Derived on read; the record is never rewritten here.
		default:
			return fmt.Errorf("store channel %q needs a v%d->v%d converter; none registered",
				strings.TrimSuffix(key, "/"), have, want.version)
		}
		stored[key] = want.version
	}
	if len(bust) > 0 {
		if err := clearDerived(trunks, bust); err != nil {
			return err
		}
	}
	return writeSchema(root, stored)
}

// clearDerived drops every derived-cache channel belonging to one of keys,
// across every trunk. Clearing is a directory removal and regeneration is
// lazy, so a bump stays cheap even on a store with hundreds of arias.
func clearDerived(trunks *xwal.Store, keys []string) error {
	for _, ti := range trunks.List() {
		x, err := trunks.Head(ti.ID)
		if err != nil {
			return err
		}
		err = func() error {
			defer x.Close()
			for _, ch := range x.Channels() {
				if _, key, ok := schemaFor(ch.Name); ok && slices.Contains(keys, key) {
					if err := x.Clear(ch.Name); err != nil {
						return fmt.Errorf("clear %s %s: %w", ti.ID, ch.Name, err)
					}
				}
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

// SchemaReport pairs a channel's on-disk schema with what this binary knows.
// Status is "ok", "behind" (migrates on next open) or "ahead" (refuses).
type SchemaReport struct {
	Channel string
	OnDisk  int // 0 when the sidecar has no entry yet
	Known   int
	Status  string
}

// SchemaStatus reads the sidecar directly, without opening the store — so it
// still answers when ensureSchema is precisely what refused the open.
func SchemaStatus(root string) ([]SchemaReport, error) {
	stored, err := readSchema(root)
	if err != nil {
		return nil, err
	}
	out := make([]SchemaReport, 0, len(channelSchemas))
	for key, want := range channelSchemas {
		r := SchemaReport{Channel: strings.TrimSuffix(key, "/"), OnDisk: stored[key], Known: want.version, Status: "ok"}
		switch {
		case r.OnDisk > r.Known:
			r.Status = "ahead"
		case r.OnDisk != 0 && r.OnDisk < r.Known:
			r.Status = "behind"
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b SchemaReport) int { return strings.Compare(a.Channel, b.Channel) })
	return out, nil
}
