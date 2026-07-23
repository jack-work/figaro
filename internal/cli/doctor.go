package cli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/transport"
)

// deadChannels are store channels no current code reads or writes:
// translations/* was replaced by translations-v2/*, turn-wal by drain +
// tail repair, _live by the transcript pivot. GC edits the figwal
// manifest directly as a stopgap until the memory-first figwal exposes
// channel removal; it therefore requires the daemon to be stopped.
func deadChannel(name string) bool {
	return name == "turn-wal" || name == "_live" ||
		(strings.HasPrefix(name, "translations/") && !strings.HasPrefix(name, "translations-v2/"))
}

func runDoctorGC(dryRun bool) error {
	if cli, err := angelus.DialClient(transport.UnixEndpoint(angelusSocketPath())); err == nil {
		cli.Close()
		return fmt.Errorf("angelus is running; stop it first (figaro stop)")
	}
	root := filepath.Join(stateDir(), "arias")
	manPath := filepath.Join(root, "xwal.json")
	raw, err := os.ReadFile(manPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("no store; nothing to do")
			return nil
		}
		return err
	}
	var man map[string]json.RawMessage
	if err := json.Unmarshal(raw, &man); err != nil {
		return fmt.Errorf("parse %s: %w", manPath, err)
	}
	var channels []map[string]any
	if err := json.Unmarshal(man["channels"], &channels); err != nil {
		return fmt.Errorf("parse channels: %w", err)
	}

	kept := channels[:0]
	var dead []string
	for _, ch := range channels {
		name, _ := ch["name"].(string)
		if deadChannel(name) {
			dead = append(dead, name)
		} else {
			kept = append(kept, ch)
		}
	}
	for _, entry := range []string{"turn-wal", "_live"} {
		if _, err := os.Stat(filepath.Join(root, entry)); err == nil && !contains(dead, entry) {
			dead = append(dead, entry)
		}
	}
	if len(dead) == 0 {
		fmt.Println("store clean; nothing to do")
		return nil
	}

	var freed int64
	for _, name := range dead {
		dir := filepath.Join(root, filepath.FromSlash(name))
		freed += dirSize(dir)
	}
	if dryRun {
		fmt.Printf("would remove %d dead channel(s) (%s): %s\n", len(dead), fmtBytes(freed), strings.Join(dead, ", "))
		return nil
	}

	enc, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	man["channels"] = enc
	out, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	tmp := manPath + ".gc-tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, manPath); err != nil {
		return err
	}
	for _, name := range dead {
		if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	fmt.Printf("removed %d dead channel(s), freed %s: %s\n", len(dead), fmtBytes(freed), strings.Join(dead, ", "))
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			n += info.Size()
		}
		return nil
	})
	return n
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}
