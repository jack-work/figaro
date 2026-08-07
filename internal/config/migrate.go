package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/BurntSushi/toml"
)

// Replaced in place: a TOML round-trip would drop comments and key order.
var legacyDefaultOutfitKey = regexp.MustCompile(`(?m)^([ \t]*)default_loadout([ \t]*=)`)

// MigrateLegacyOutfits renames the pre-rename loadouts/ directory to outfits/
// and default_loadout to default_outfit, returning a line per change. It
// declines rather than guesses: where both spellings exist, neither is touched.
func MigrateLegacyOutfits(configDir string) ([]string, error) {
	var done []string

	legacyDir := filepath.Join(configDir, "loadouts")
	canonicalDir := filepath.Join(configDir, "outfits")
	legacyInfo, err := os.Stat(legacyDir)
	if err != nil && !os.IsNotExist(err) {
		return done, fmt.Errorf("stat %s: %w", legacyDir, err)
	}
	if err == nil && legacyInfo.IsDir() {
		if _, cErr := os.Stat(canonicalDir); os.IsNotExist(cErr) {
			if rErr := os.Rename(legacyDir, canonicalDir); rErr != nil {
				return done, fmt.Errorf("move %s to %s: %w", legacyDir, canonicalDir, rErr)
			}
			done = append(done, fmt.Sprintf("moved %s → %s", legacyDir, canonicalDir))
		}
	}

	configPath := filepath.Join(configDir, "config.toml")
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return done, nil
	}
	if err != nil {
		return done, fmt.Errorf("read %s: %w", configPath, err)
	}
	if !legacyDefaultOutfitKey.Match(data) {
		return done, nil
	}
	var probe struct {
		DefaultOutfit string `toml:"default_outfit"`
	}
	if pErr := toml.Unmarshal(data, &probe); pErr != nil {
		return done, fmt.Errorf("parse %s: %w", configPath, pErr)
	}
	if probe.DefaultOutfit != "" {
		return done, nil
	}
	patched := legacyDefaultOutfitKey.ReplaceAll(data, []byte("${1}default_outfit${2}"))
	if err := writeFileAtomic(configPath, patched, configPath); err != nil {
		return done, err
	}
	return append(done, fmt.Sprintf("renamed default_loadout → default_outfit in %s", configPath)), nil
}

// Temp file plus rename, so an interrupt cannot half-write a config. The
// chmod is best-effort: CreateTemp's 0600 is the safe outcome if it fails.
func writeFileAtomic(path string, data []byte, modeOf string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if info, sErr := os.Stat(modeOf); sErr == nil {
		_ = tmp.Chmod(info.Mode().Perm())
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpName, path, err)
	}
	tmpName = ""
	return nil
}
