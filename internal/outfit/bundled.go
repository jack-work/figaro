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

// The first-party skills ship INSIDE the binary (see skills.go at the repo
// root) and are unpacked onto disk here, because a skill without a path is
// half a skill: the form hands the model a `filePath`, and the model follows
// links from that file to the chapters beside it. Holding the bytes in memory
// would give the model a body it cannot navigate.
//
// The unpack directory is named by the CONTENT HASH of the embedded tree, so:
//
//   - two figaro versions with different skills never fight over one path;
//   - an upgrade MOVES the root, which is exactly the signal the angelus
//     already watches to remint the default form (see noticeUpgrade);
//   - a rebuild that did not change a skill lands on the same path and costs
//     one stat.
//
// Nothing prunes old hash directories. An older binary may still be running
// against its own root, and a form minted last week records the filePath it
// was composed with; deleting that tree to reclaim a few hundred kilobytes
// would break reads that still resolve today.

var (
	bundledMu      sync.Mutex
	bundledEnabled = true
	bundledRoot    string
	bundledKnown   bool
)

// SetBundledSkills turns the binary's own skills on or off. Off means figaro
// composes forms from the user's config dir ALONE, which is what someone who
// maintains their own copy of the figaro skill wants: config.toml's
// `bundled_skills = false` reaches this. Startup calls it once, before any
// outfit is folded.
//
// FIGARO_BUNDLED_SKILLS still wins over this: an env var is the tool you have
// when the config file is the thing you are debugging.
func SetBundledSkills(enabled bool) {
	bundledMu.Lock()
	defer bundledMu.Unlock()
	if enabled != bundledEnabled {
		bundledEnabled = enabled
		bundledKnown = false
		bundledRoot = ""
	}
}

// BundledSkillsRoot is where the binary's own first-party skills live. The
// daemon records it with the default form: when it moves, the shipped skills
// moved with it, and the default form is due for recomputation.
func BundledSkillsRoot() string { return bundledSkillsRoot() }

// bundledSkillsRoot returns the directory whose child `skills/` holds the
// first-party skills, so a `dirName = "skills"` reference resolves to
// <root>/skills/figaro. Precedence, highest first:
//
//	FIGARO_BUNDLED_SKILLS=<path>   use that root verbatim (tests, dev trees)
//	FIGARO_BUNDLED_SKILLS=0|off    no bundled skills at all
//	SetBundledSkills(false)        no bundled skills at all (config.toml)
//	otherwise                      unpack the embedded tree and use that
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
		// Not fatal, and not silent either. figaro still runs on the user's
		// own skills; it just runs without its own, and the reason says so.
		slog.Warn("bundled skills could not be unpacked: figaro will compose forms without them", "err", err)
		root = ""
	}
	bundledRoot, bundledKnown = root, true
	return root
}

// bundledSkillsDir is the parent that holds one directory per content hash.
// It follows internal/cli's stateDir precedence on purpose: a dev shell that
// isolates FIGARO_STATE_DIR must get its own unpacked skills too, or two
// figaros with different skills would read one tree.
//
// State, not cache: a form records the filePath it was composed with, so this
// tree is referenced by data that outlives the process. A cache is a place
// things are allowed to disappear from.
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

// unpackBundledSkills writes the embedded tree under dir/<hash> and returns
// that root. It is idempotent: a root whose .stamp already names the hash is
// complete and is returned untouched.
//
// The write is staged in a sibling temp directory and renamed into place, so
// a process killed mid-unpack leaves a `.unpack-*` directory behind rather
// than a half-populated root that the next run would happily read as whole.
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
		// Read-only on purpose: an edit here is lost at the next version
		// bump, because the next version unpacks to a different root. Skills
		// you own belong in your config dir, where nothing overwrites them.
		return os.WriteFile(dst, body, 0o444)
	})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, ".stamp"), []byte(hash+"\n"), 0o444); err != nil {
		return "", err
	}

	// A leftover root here is incomplete by definition: its stamp did not
	// match above. Clear it before the rename, which will not replace a
	// non-empty directory.
	if err := os.RemoveAll(root); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, root); err != nil {
		// Another figaro unpacking the same hash at the same moment is a
		// race we lose gladly: the bytes are identical, and its stamp is
		// the proof that it finished.
		if b, rerr := os.ReadFile(stamp); rerr == nil && strings.TrimSpace(string(b)) == hash {
			return root, nil
		}
		return "", err
	}
	return root, nil
}

// embeddedSkillsHash digests every embedded path and its bytes. Paths are
// part of the digest because moving a chapter changes what the model can
// follow even when no byte of prose changed; WalkDir is lexical, so the
// digest is stable across machines and builds.
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
