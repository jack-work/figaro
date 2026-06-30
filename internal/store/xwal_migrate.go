package store

// In-place migration from the pre-root/stumps layout to root/stumps.
//
// Old layout: the root ir dir carries a .trunk marker (the "null" trunk id),
// loadouts are depth-1 ir nodes with their own .trunk markers, and a small
// policy.json holds {null, loadouts: "name@ver" -> trunk id}.
//
// New layout: the root is markerless; each loadout is a markerless stump named
// "<name>@<ver>"; the dedup map lives on disk (the stump names). This migration
// renames each loadout node to its stump name across the ir + chalkboard
// channels, drops the loadout + root .trunk markers, and drops the (derivable,
// historically sparse) translations channel so it re-backfills correctly on
// next use. It is idempotent and crash-safe: the root .trunk is removed last,
// so an interrupted run re-completes on the next open.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// migrateToStumps heals a pre-root/stumps store in place. No-op if the store is
// fresh or already migrated (the root carries no .trunk marker).
func migrateToStumps(root string) error {
	irRoot := filepath.Join(root, chanIR)
	rootMarker := filepath.Join(irRoot, ".trunk")
	if !fileExists(rootMarker) {
		return nil // markerless root => fresh or already migrated
	}
	pol, err := readLegacyPolicy(root)
	if err != nil {
		return err
	}
	rev := make(map[string]string, len(pol.Loadouts)) // loadout trunk id -> "name@ver"
	for nameVer, tid := range pol.Loadouts {
		rev[tid] = nameVer
	}

	// Rename each loadout node (a depth-1 ir node whose .trunk id is a known
	// loadout) to its stump name across ir + chalkboard, dropping its marker.
	ents, err := os.ReadDir(irRoot)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		tid, ok := readTrunkMarker(filepath.Join(irRoot, e.Name()))
		if !ok {
			continue
		}
		stump, isLoadout := rev[tid]
		if !isLoadout {
			continue // a loadoutless top-level conversation: leave it a trunk
		}
		for _, ch := range []string{chanIR, chanChalkboard} {
			src := filepath.Join(root, ch, e.Name())
			dst := filepath.Join(root, ch, stump)
			if fileExists(src) && !fileExists(dst) {
				if err := os.Rename(src, dst); err != nil {
					return fmt.Errorf("rename %s -> %s: %w", src, dst, err)
				}
			}
		}
		// Drop the loadout's marker (under its new stump name): it becomes a
		// markerless stump identified by name + depth-1 position.
		if err := os.Remove(filepath.Join(irRoot, stump, ".trunk")); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	// Drop the derivable translations cache (re-backfills correctly on next use).
	if err := dropTranslationsChannels(root); err != nil {
		return err
	}
	// The on-disk stump names supersede the policy side-file.
	if err := os.WriteFile(filepath.Join(root, "policy.json"), []byte("{}\n"), 0o644); err != nil {
		return err
	}
	// Root sheds its marker LAST — the completion signal.
	if err := os.Remove(rootMarker); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

type mfChannel struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Reducer string `json:"reducer,omitempty"`
}

type mfManifest struct {
	Main     string      `json:"main"`
	Codec    string      `json:"codec"`
	Channels []mfChannel `json:"channels"`
}

// dropTranslationsChannels removes every "translations/*" channel from the
// manifest and deletes its on-disk tree. The translation cache is derivable
// (re-translated from IR) and the old layout's was sparse/mis-rooted, so the
// clean re-backfill on next OpenTranslation is the correct heal.
func dropTranslationsChannels(root string) error {
	mpath := filepath.Join(root, "xwal.json")
	data, err := os.ReadFile(mpath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var m mfManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	kept := make([]mfChannel, 0, len(m.Channels))
	for _, c := range m.Channels {
		if strings.HasPrefix(c.Name, "translations/") {
			continue
		}
		kept = append(kept, c)
	}
	m.Channels = kept
	body, _ := json.MarshalIndent(m, "", "  ")
	tmp := mpath + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, mpath); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(root, "translations"))
}

func readLegacyPolicy(root string) (policy, error) {
	data, err := os.ReadFile(filepath.Join(root, "policy.json"))
	if os.IsNotExist(err) {
		return policy{}, nil
	}
	if err != nil {
		return policy{}, err
	}
	var p policy
	if err := json.Unmarshal(data, &p); err != nil {
		return policy{}, fmt.Errorf("parse legacy policy: %w", err)
	}
	return p, nil
}

func readTrunkMarker(nodeDir string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(nodeDir, ".trunk"))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
