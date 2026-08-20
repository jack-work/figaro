package store

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/store/xwal"
)

// schemaFileName is figaro's channel-schema sidecar, kept next to figwal's
// xwal.json rather than inside it. It cannot live in the manifest: figwal
// owns that file, and its manifestChannel struct is {name,kind,reducer,opaque}
// re-marshalled on every channel add, any field we wrote there would be
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
	// v2: the role vocabulary became input/output (was user/assistant). Reading
	// old entries is transparent: message.Role.UnmarshalJSON normalises via
	// RoleFromWire: so canonical data needs no migration. The bump exists for
	// the OTHER direction: an older binary has no such mapping and would read
	// "input" as an unknown voice, rendering turns under the wrong speaker. The
	// forward-incompatibility gate turns that silent corruption into a refusal.
	// v3: input messages carry a `steering` flag distinguishing a mid-turn
	// direction from a new question. Reading old entries is transparent (the
	// flag is absent, and the legacy prose-on-a-tool_result shape is still
	// recognised as steering), so canonical data needs no migration. The bump
	// is again for the OTHER direction: an older binary ignores the flag, so a
	// steer would open a spurious turn and truncate the exchange it belongs
	// to: shifting every turn id after it, which is the coordinate
	// `send`/`fork <trunk>:<turn>` addresses. The gate makes that a refusal.
	// v4: a main record carries a CURSOR STAMP -- where each unkeyed channel
	// stood when the record was written -- and the form became unkeyed
	// in the same release, so its records now carry main LT 0 instead of a
	// real one. Reading an old store is transparent: a record with no stamp
	// falls back to deriving the boundary from the form's old main-LT
	// key, so nothing is rewritten and no converter is needed.
	chanIR:   {version: 4, class: classCanonical},
	chanForm: {version: 1, class: classReducible},
	// v2: not a shape change -- a POISON sweep. A projection bug rendered the
	// whole form onto one message per provider round-trip instead of the
	// delta, and the encoder wrote that into these per-LT caches, so the
	// duplication is durable and re-reading does not undo it. Measured on real
	// arias: 31-60% of a conversation's cached bytes were repeated board, and
	// six seats sat at 95-101% of their context limit because of it.
	// Bumping a derived channel drops it; the projection regenerates it lazily
	// and correctly. Nothing canonical moves.
	// v3: a row's sidecar carries the CONTENT HASH of the fig IR record it
	// translates, beside the fingerprint. v2 rows have no hash and cannot
	// grow one, so they are dropped and re-derived -- which is what the
	// derived class is for.
	"translations-v2/": {version: 3, class: classDerived},
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

// storeVersion is the generation of MEANING this build writes: not the shape
// of a record (that is a channel schema) and not the arrangement on disk (that
// is figwal's layout), but what correctly-shaped data is taken to mean.
const storeVersion = 2

type schemaFile struct {
	StoreVersion int            `json:"store-version"`
	Channels     map[string]int `json:"channels"`
}

func readSchema(root string) (schemaFile, error) {
	raw, err := os.ReadFile(filepath.Join(root, schemaFileName))
	if os.IsNotExist(err) {
		return schemaFile{Channels: map[string]int{}}, nil
	}
	if err != nil {
		return schemaFile{}, err
	}
	var f schemaFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return schemaFile{}, fmt.Errorf("store schema: %w", err)
	}
	if f.Channels == nil {
		f.Channels = map[string]int{}
	}
	return f, nil
}

func writeSchema(root string, f schemaFile) error {
	raw, err := json.MarshalIndent(f, "", "  ")
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
func ensureSchema(root string, trunks *xwal.Store) error {
	f, err := readSchema(root)
	if err != nil {
		return err
	}
	return checkGeneration(f, root, trunks)
}

// checkGeneration is the version half, split out so it can run BEFORE figwal
// opens the store. A generation-1 store names its form channel
// chanFormLegacy, and figwal's manifest is authoritative for channel shape:
// so it refuses the open itself, with "no reducer \"jsonmerge\" registered
// for channel \"chalkboard\"", which explains nothing to the person holding
// the store. This must speak first.
func checkGeneration(f schemaFile, root string, trunks *xwal.Store) error {
	if f.StoreVersion > storeVersion {
		return fmt.Errorf(
			"store is generation %d but this figaro understands %d: "+
				"refusing to open a store written by a newer build (upgrade figaro)",
			f.StoreVersion, storeVersion)
	}
	if trunks == nil {
		return nil // pre-open pass: the version is all that can be checked
	}
	f.StoreVersion = storeVersion
	stored := f.Channels
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
	f.Channels = stored
	return writeSchema(root, f)
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

// StoreGeneration reports the store's recorded generation and this build's,
// read straight from the sidecar so it answers even when the gate refused.
func StoreGeneration(root string) (onDisk, known int, err error) {
	f, err := readSchema(root)
	return f.StoreVersion, storeVersion, err
}

// CheckStoreGeneration refuses a store this build cannot read, and migrates one
// it can. It runs before anything OPENS the store, which is not a nicety: a
// generation whose channels are named differently makes figwal refuse first,
// with a message about a missing reducer, and a migration cannot run against an
// open store anyway.
func CheckStoreGeneration(root string) error {
	f, err := readSchema(root)
	if err != nil {
		return err
	}
	if err := checkGeneration(f, root, nil); err != nil {
		return err
	}
	return migrateGenerations(root, f.StoreVersion)
}

// isNoSuchChannel is the one refusal a re-run may ignore: the channel is gone
// because this migration already moved it.
func isNoSuchChannel(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such channel")
}

// generationMigration turns a store of generation N-1 into one of generation N.
// It runs with the store closed, and must be idempotent: the sidecar is stamped
// only after every step has succeeded, so a crash re-runs the whole chain.
type generationMigration struct {
	to  int
	run func(root string) error
}

// generationMigrations is the registry the storeVersion comment promised -
// keyed on the number, not on a probe. Absence is meaningful: generation 1
// changed no bytes, so nothing runs for it.
var generationMigrations = []generationMigration{
	{to: 2, run: migrateFormChannel},
}

func migrateGenerations(root string, from int) error {
	// No manifest, no store: a directory becomes one when figwal creates it,
	// with today's channel names. Nothing to move, and reading a manifest that
	// is not there would fail every first open.
	if _, err := os.Stat(filepath.Join(root, "xwal.json")); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if from == 0 {
		// A store with a manifest and no sidecar predates the sidecar, which
		// makes it generation 1 by construction. Each migration is a no-op when
		// its subject is already absent, so starting the chain costs nothing.
		from = 1
	}
	for _, m := range generationMigrations {
		if from >= m.to {
			continue
		}
		start := time.Now()
		if err := m.run(root); err != nil {
			return fmt.Errorf("store %s: migration to generation %d failed: %w", root, m.to, err)
		}
		slog.Info("store generation migrated", "root", root,
			"to", m.to, "ms", time.Since(start).Milliseconds())
	}
	return nil
}

// chanFormLegacy is the generation-1 name of the form channel. It is a
// constant so that renaming chanForm cannot silently rewrite the old name a
// migration exists to recognise.
const chanFormLegacy = "chalkboard"

// migrateFormChannel is generation 1 -> 2: the form's channel directory was
// called chanFormLegacy. Renaming it is figwal's job (the manifest is its
// format); the sidecar's channel key is ours.
func migrateFormChannel(root string) error {
	if _, err := os.Stat(filepath.Join(root, chanFormLegacy)); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		// Nothing under the old name. Either this store never had one, or a
		// previous run moved it and died before the sidecar was stamped, and
		// RenameChannel repairs the manifest for that second case.
		if rerr := xwal.RenameChannel(root, chanFormLegacy, chanForm); rerr != nil && !isNoSuchChannel(rerr) {
			return rerr
		}
	} else if err := xwal.RenameChannel(root, chanFormLegacy, chanForm); err != nil {
		return err
	}
	f, err := readSchema(root)
	if err != nil {
		return err
	}
	if f.Channels == nil {
		return nil
	}
	if v, ok := f.Channels[chanFormLegacy]; ok {
		delete(f.Channels, chanFormLegacy)
		if _, taken := f.Channels[chanForm]; !taken {
			f.Channels[chanForm] = v
		}
		return writeSchema(root, f)
	}
	return nil
}

// SchemaStatus reads the sidecar directly, without opening the store: so it
// still answers when ensureSchema is precisely what refused the open.
func SchemaStatus(root string) ([]SchemaReport, error) {
	f, err := readSchema(root)
	if err != nil {
		return nil, err
	}
	stored := f.Channels
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
