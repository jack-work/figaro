package cli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/transport"
)

// deadChannels name store channels nothing reads or writes: turn-wal (drain +
// tail repair replaced it) and _live (the transcript pivot); both are scanned
// on disk too, since _live was never a manifest entry. Legacy translations/* is
// swept by prefix — translations-v2/ does not carry it. GC rewrites the figwal
// manifest directly, so it requires the daemon stopped.
var deadChannels = []string{"turn-wal", "_live"}

func deadChannel(name string) bool {
	return slices.Contains(deadChannels, name) || strings.HasPrefix(name, "translations/")
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
	for _, entry := range deadChannels {
		if _, err := os.Stat(filepath.Join(root, entry)); err == nil && !slices.Contains(dead, entry) {
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
		fmt.Printf("would remove %d dead channel(s) (%s): %s\n", len(dead), tool.FormatSize(int(freed)), strings.Join(dead, ", "))
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
		dir := filepath.Join(root, filepath.FromSlash(name))
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove %s: %w", name, err)
		}
		if strings.Contains(name, "/") {
			_ = os.Remove(filepath.Dir(dir)) // drop the legacy parent once empty
		}
	}
	fmt.Printf("removed %d dead channel(s), freed %s: %s\n", len(dead), tool.FormatSize(int(freed)), strings.Join(dead, ", "))
	return nil
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
