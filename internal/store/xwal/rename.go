package xwal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jack-work/figaro/internal/store/disk"
)

// RenameChannel renames a channel on disk: its directory under root, and its
// entry in the manifest. It is for a CONSUMER's migration — figaro renaming its
// form channel — and it is here rather than there because the manifest is
// figwal's private format, and old-format knowledge belongs in the code that
// owns the format.
//
// It must run with the store CLOSED. There is no lock to take: an open store
// holds file handles on the directory being moved, and on the manifest it would
// then be told to disbelieve. The caller runs it in the same place a layout
// migration runs, before anything opens the store.
//
// It is idempotent in the useful direction. A store already renamed (no `from`
// in the manifest, `to` present) reports nothing to do rather than failing, so a
// migration that dies between the move and the manifest write can be re-run:
// the directory move happens first and the manifest is repaired second, and
// either half alone is recognised.
//
// The reducer NAME travels with the channel when it matched the old channel
// name. A reducer is resolved by its own name first and the channel's second
// (resolveReducer), so a channel called "x" with reducer "x" renamed to "y"
// must look for "y" — otherwise the consumer registers its reducer under the
// new name and the open fails with "no reducer registered", which is exactly
// the failure this whole migration exists to avoid.
func RenameChannel(root, from, to string) error {
	if err := validChannelName(from); err != nil {
		return fmt.Errorf("xwal: rename channel: from: %w", err)
	}
	if err := validChannelName(to); err != nil {
		return fmt.Errorf("xwal: rename channel: to: %w", err)
	}
	if from == to {
		return nil
	}
	m, err := readManifestFile(root)
	if err != nil {
		return err
	}
	var found bool
	for _, c := range m.Channels {
		switch c.Name {
		case from:
			found = true
		case to:
			if !found {
				return nil // already renamed
			}
			return fmt.Errorf("xwal: rename channel %q -> %q: both exist", from, to)
		}
	}
	if !found {
		return fmt.Errorf("xwal: rename channel %q: no such channel", from)
	}

	// The directory first, then the manifest. A crash between them leaves a
	// store whose manifest still says `from` and whose data sits at `to`, and
	// the pass above recognises that: `from` is in the manifest, the move is a
	// no-op because the source is gone, and the manifest write completes.
	src, dst := filepath.Join(root, filepath.FromSlash(from)), filepath.Join(root, filepath.FromSlash(to))
	if _, err := os.Stat(src); err == nil {
		if _, err := os.Stat(dst); err == nil {
			return fmt.Errorf("xwal: rename channel %q -> %q: %s already exists", from, to, dst)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("xwal: rename channel dir: %w", err)
		}
		// Both parents: the entry left one directory and joined another, and a
		// crash must not find the move half-durable.
		if err := disk.SyncDir(filepath.Dir(dst)); err != nil {
			return err
		}
		if err := disk.SyncDir(filepath.Dir(src)); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	for i := range m.Channels {
		if m.Channels[i].Name != from {
			continue
		}
		if m.Channels[i].Reducer == from {
			m.Channels[i].Reducer = to
		}
		m.Channels[i].Name = to
	}
	if m.Main == from {
		m.Main = to
	}
	return writeManifest(root, m)
}

// validChannelName is the manifest's own rule, minus the reserved-component
// check that only applies to a channel being declared: a rename may not name a
// path that climbs out of the store or hides in a dotfile.
func validChannelName(name string) error {
	native := filepath.FromSlash(name)
	if name == "" || filepath.IsAbs(native) || filepath.Clean(native) != native ||
		native == "." || native == ".." || strings.HasPrefix(native, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid channel name %q", name)
	}
	for _, component := range strings.FieldsFunc(native, func(r rune) bool { return r == '/' || r == '\\' }) {
		if strings.HasPrefix(component, ".") || component == manifestName {
			return fmt.Errorf("reserved channel path component %q", component)
		}
	}
	return nil
}
