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

// schemaFileName sits beside figwal's xwal.json, not inside it: figwal
// re-marshals the manifest on every channel add and would drop any field
// written there.
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

// channelSchemas: a key ending in "/" matches by prefix. Bump a version when
// a channel's on-disk payload shape changes.
var channelSchemas = map[string]channelSchema{
	// v2: roles became input/output; old entries read transparently, the bump
	// refuses an older binary that would read "input" as an unknown voice.
	// v3: input messages carry `steering`; an older binary would open a
	// spurious turn and shift every turn id after it.
	// v4: a main record carries a cursor stamp and the form became unkeyed
	// (main LT 0); a record with no stamp derives the boundary from the
	// form's old main-LT key.
	// v5: turn_id is explicit; omitempty made zero indistinguishable from
	// never written.
	chanIR: {version: 5, class: classCanonical},
	// v2: patches are structural; a flat one can neither reach a nested field
	// nor be inverted. MigrateFormToStructural converts.
	chanForm: {version: 2, class: classReducible},
	// v2: a poison sweep, not a shape change: a projection bug wrote the whole
	// form into these caches. Bumping a derived channel drops it and the
	// projection regenerates it.
	// v3: rows carry the content hash of the IR record they translate; v2
	// rows cannot grow one and are re-derived.
	"translations-v2/": {version: 3, class: classDerived},
	chanUI:             {version: 1, class: classDerived},
}

// schemaFor returns the registry key that matched: the key, not the name, is
// what the sidecar records, so prefix families version as a unit.
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

// storeVersion is the generation of meaning this build writes: not a record's
// shape (channel schema) nor the arrangement on disk (figwal's layout).
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

func ensureSchema(root string, trunks *xwal.Store) error {
	f, err := readSchema(root)
	if err != nil {
		return err
	}
	return checkGeneration(f, root, trunks)
}

// checkGeneration is split out so it can run before figwal opens the store: a
// generation-1 store names its form channel chanFormLegacy, and figwal would
// refuse first with a message about a missing reducer.
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
		case len(channelMigrations[key]) > 0:
			// migrateChannels ran before the open; reaching here means it did
			// not raise the version, a bug in the migration.
			return fmt.Errorf("store channel %q is still v%d after migrating to v%d",
				strings.TrimSuffix(key, "/"), have, want.version)
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

// clearDerived is a directory removal and regeneration is lazy, so a bump stays cheap.
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

// SchemaReport.Status is "ok", "behind" (migrates on next open) or "ahead" (refuses).
type SchemaReport struct {
	Channel string
	OnDisk  int // 0 when the sidecar has no entry yet
	Known   int
	Status  string
}

// StoreGeneration reads the sidecar directly, so it answers even when the gate refused.
func StoreGeneration(root string) (onDisk, known int, err error) {
	f, err := readSchema(root)
	return f.StoreVersion, storeVersion, err
}

// CheckStoreGeneration runs before anything opens the store: figwal would
// refuse a differently-named channel first, and a migration cannot run
// against an open store.
func CheckStoreGeneration(root string) error {
	f, err := readSchema(root)
	if err != nil {
		return err
	}
	if err := checkGeneration(f, root, nil); err != nil {
		return err
	}
	if err := migrateGenerations(root, f.StoreVersion); err != nil {
		return err
	}
	return migrateChannels(root, f.Channels)
}

// isNoSuchChannel is the one refusal a re-run may ignore: this migration
// already moved the channel.
func isNoSuchChannel(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such channel")
}

// generationMigration runs with the store closed and must be idempotent: the
// sidecar is stamped only after every step, so a crash re-runs the chain.
type generationMigration struct {
	to  int
	run func(root string) error
}

// Absence is meaningful: generation 1 changed no bytes.
var generationMigrations = []generationMigration{
	{to: 2, run: migrateFormChannel},
}

// channelMigration: same contract as generationMigration.
type channelMigration struct {
	to  int
	run func(root string) error
}

// channelMigrations is keyed like channelSchemas and ordered: a store several
// versions behind runs each step in turn.
var channelMigrations = map[string][]channelMigration{
	chanForm: {
		{to: 2, run: MigrateFormToStructural},
	},
	chanIR: {
		{to: 5, run: MigrateStampTurnIDs},
	},
}

// migrateChannels runs before anything opens the store.
func migrateChannels(root string, stored map[string]int) error {
	if _, err := os.Stat(filepath.Join(root, "xwal.json")); err != nil {
		if os.IsNotExist(err) {
			return nil // a directory becomes a store on first write
		}
		return err
	}
	keys := make([]string, 0, len(channelMigrations))
	for k := range channelMigrations {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		from, seen := stored[key]
		if !seen {
			// No sidecar entry: a fresh store (every migration a no-op) or one
			// predating the sidecar, the oldest version by construction.
			from = 0
		}
		for _, m := range channelMigrations[key] {
			if from >= m.to {
				continue
			}
			start := time.Now()
			if err := m.run(root); err != nil {
				return fmt.Errorf("store %s: channel %q migration to v%d failed: %w",
					root, strings.TrimSuffix(key, "/"), m.to, err)
			}
			// Stamped per step, not per chain: a crash then resumes at the
			// step it died on rather than redoing the ones that landed.
			if err := stampChannel(root, key, m.to); err != nil {
				return err
			}
			from = m.to
			slog.Info("channel migrated", "root", root,
				"channel", strings.TrimSuffix(key, "/"), "to", m.to,
				"ms", time.Since(start).Milliseconds())
		}
	}
	return nil
}

func stampChannel(root, key string, to int) error {
	f, err := readSchema(root)
	if err != nil {
		return err
	}
	if f.Channels == nil {
		f.Channels = map[string]int{}
	}
	f.Channels[key] = to
	return writeSchema(root, f)
}

func migrateGenerations(root string, from int) error {
	// No manifest, no store: figwal creates one with today's channel names.
	if _, err := os.Stat(filepath.Join(root, "xwal.json")); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if from == 0 {
		// A manifest with no sidecar is generation 1 by construction.
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

// chanFormLegacy is a constant so renaming chanForm cannot silently rewrite
// the old name a migration exists to recognise.
const chanFormLegacy = "chalkboard"

// migrateFormChannel is generation 1 -> 2. Renaming the directory is figwal's
// job (the manifest is its format); the sidecar key is ours.
func migrateFormChannel(root string) error {
	if _, err := os.Stat(filepath.Join(root, chanFormLegacy)); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		// Nothing under the old name: never had one, or a previous run moved it
		// and died before the stamp; RenameChannel repairs the manifest then.
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

// SchemaStatus reads the sidecar without opening the store, so it answers
// when ensureSchema is what refused.
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
