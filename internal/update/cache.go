package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Cache is the on-disk memo of a Check(), so successive CLI
// invocations don't hammer the module proxy.
type Cache struct {
	dir string
}

// NewCache returns a Cache rooted at dir (usually $XDG_CACHE_HOME/figaro).
// The directory is created lazily on write; Read on a missing tree is a
// miss, not an error.
func NewCache(dir string) *Cache { return &Cache{dir: dir} }

func (c *Cache) path() string { return filepath.Join(c.dir, "update-check.json") }

// Read returns the cached Info if it exists and is younger than ttl.
// A miss (no file, unreadable, expired, unparseable) returns nil with
// no error — the caller should treat it as "check now".
//
// ttl <= 0 always misses (the caller wants a fresh check).
func (c *Cache) Read(ttl time.Duration) *Info {
	if c == nil || c.dir == "" {
		return nil
	}
	if ttl <= 0 {
		return nil
	}
	data, err := os.ReadFile(c.path())
	if err != nil {
		return nil
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return nil
	}
	if time.Since(info.CheckedAt) > ttl {
		return nil
	}
	return &info
}

// Write persists the Info. Errors are returned but callers typically
// log-and-continue: a failed cache write is never fatal.
func (c *Cache) Write(info *Info) error {
	if c == nil || c.dir == "" {
		return errors.New("update: nil cache")
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path())
}

// Check returns cached Info if fresh; otherwise fetches from the
// module proxy, writes the cache, and returns the new Info. Never
// blocks longer than the FetchLatest client timeout (~5s) on the wire.
//
// currentVersion is what to compare against; pass the ldflags-injected
// commit if you don't have a real semver.
// module is e.g. "github.com/jack-work/figaro".
func Check(ctx context.Context, cache *Cache, ttl time.Duration, module, currentVersion string) *Info {
	if info := cache.Read(ttl); info != nil {
		// Recompute Available in case the running binary was upgraded
		// out from under the cached record.
		info.Current = currentVersion
		info.Available = info.Latest != "" && Compare(info.Latest, currentVersion) > 0
		return info
	}
	info := &Info{
		Current:   currentVersion,
		Channel:   DetectChannel(),
		CheckedAt: time.Now(),
	}
	if exe, err := os.Executable(); err == nil {
		info.Exe = exe
	}
	latest, err := FetchLatest(ctx, module)
	if err != nil {
		info.FetchError = err.Error()
	} else {
		info.Latest = latest
		info.Available = Compare(latest, currentVersion) > 0
	}
	_ = cache.Write(info)
	return info
}

// Nudge returns a one-line message when a newer version is available
// on a channel we can point at, "" otherwise. Formatted to stand out
// briefly in stderr without dominating the CLI's real output.
func Nudge(info *Info, module string) string {
	if info == nil || !info.Available || info.Latest == "" {
		return ""
	}
	msg := fmt.Sprintf("figaro %s → %s available", info.Current, info.Latest)
	if cmd := UpgradeCommand(info.Channel, module, info.Latest); cmd != "" {
		msg += "  ·  run: " + cmd
	} else {
		msg += "  ·  run: figaro update"
	}
	return msg
}
