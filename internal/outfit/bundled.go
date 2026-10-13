package outfit

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	figaro "github.com/jack-work/figaro"
)

// Embedded skills are unpacked to disk: the form hands the model a filePath
// and the model follows links from it.

var (
	bundledMu      sync.Mutex
	bundledEnabled = true
	bundledRoot    string
	bundledKnown   bool
)

// SetBundledSkills must be called before any outfit is folded.
func SetBundledSkills(enabled bool) {
	bundledMu.Lock()
	defer bundledMu.Unlock()
	if enabled != bundledEnabled {
		bundledEnabled = enabled
		bundledKnown = false
		bundledRoot = ""
	}
}

// BundledSkillsRoot changes when the shipped skills change; the default form is keyed on it.
func BundledSkillsRoot() string { return bundledSkillsRoot() }

// FIGARO_BUNDLED_SKILLS (off, or a root) wins over the config toggle.
func bundledSkillsRoot() string {
	if v, ok := os.LookupEnv("FIGARO_BUNDLED_SKILLS"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "0", "off", "false":
			return ""
		default:
			return v
		}
	}

	bundledMu.Lock()
	defer bundledMu.Unlock()
	if !bundledEnabled {
		return ""
	}
	if bundledKnown {
		return bundledRoot
	}
	root, err := unpackBundledSkills(bundledSkillsDir())
	if err != nil {
		slog.Warn("bundled skills could not be unpacked: figaro will compose forms without them", "err", err)
		root = ""
	}
	bundledRoot, bundledKnown = root, true
	return root
}

// Follows internal/cli's stateDir precedence: an isolated FIGARO_STATE_DIR must get its own tree.
func bundledSkillsDir() string {
	base := ""
	switch {
	case os.Getenv("FIGARO_STATE_DIR") != "":
		base = os.Getenv("FIGARO_STATE_DIR")
	case os.Getenv("XDG_STATE_HOME") != "":
		base = filepath.Join(os.Getenv("XDG_STATE_HOME"), "figaro")
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "figaro-state")
		}
		base = filepath.Join(home, ".local", "state", "figaro")
	}
	return filepath.Join(base, "bundled-skills")
}

// A root whose .stamp names the hash is complete and returned untouched.
func unpackBundledSkills(dir string) (string, error) {
	hash, err := embeddedSkillsHash()
	if err != nil {
		return "", err
	}
	root := filepath.Join(dir, hash)
	stamp := filepath.Join(root, ".stamp")
	if b, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(b)) == hash {
		return root, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(dir, ".unpack-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	err = fs.WalkDir(figaro.Skills, "skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(tmp, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		body, err := figaro.Skills.ReadFile(path)
		if err != nil {
			return err
		}
		// Read-only: the next version unpacks to a different root, so an edit here is lost.
		return os.WriteFile(dst, body, 0o444)
	})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, ".stamp"), []byte(hash+"\n"), 0o444); err != nil {
		return "", err
	}

	// A leftover root is incomplete (its stamp did not match); rename will not replace a non-empty dir.
	if err := os.RemoveAll(root); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, root); err != nil {
		// A concurrent unpack of the same hash wrote identical bytes; its stamp proves it finished.
		if b, rerr := os.ReadFile(stamp); rerr == nil && strings.TrimSpace(string(b)) == hash {
			return root, nil
		}
		return "", err
	}
	return root, nil
}

// Paths are part of the digest: moving a chapter changes what the model can follow.
func embeddedSkillsHash() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(figaro.Skills, "skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		body, err := figaro.Skills.ReadFile(path)
		if err != nil {
			return err
		}
		h.Write([]byte(path))
		h.Write([]byte{0})
		h.Write(body)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}
