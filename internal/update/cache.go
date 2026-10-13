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

type Cache struct {
	dir string
}

func NewCache(dir string) *Cache { return &Cache{dir: dir} }

func (c *Cache) path() string { return filepath.Join(c.dir, "update-check.json") }

// Read returns nil on any miss: no file, unreadable, expired, unparseable.
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

// Check serves a fresh cache or fetches, writes and returns.
func Check(ctx context.Context, cache *Cache, ttl time.Duration, module, currentVersion string) *Info {
	if info := cache.Read(ttl); info != nil {
		// The binary may have been upgraded under the cached record.
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

// Nudge is one line when a newer version is available, "" otherwise.
func Nudge(info *Info, module string) string {
	if info == nil || !info.Available || info.Latest == "" {
		return ""
	}
	if _, _, _, ok := parseSemver(info.Current); !ok {
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
