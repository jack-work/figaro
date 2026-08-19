package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/transport"
)

// deadChannels name store channels nothing reads or writes: turn-wal (drain +
// tail repair replaced it) and _live (the transcript pivot); both are scanned
// on disk too, since _live was never a manifest entry. Legacy translations/* is
// swept by prefix: translations-v2/ does not carry it. GC rewrites the figwal
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
	root := ariaRoot()
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

// runDoctorSchema reports each channel's on-disk schema against what this
// binary understands. It reads the sidecar directly, so it still answers when
// the schema gate is exactly what refused to open the store.
func runDoctorSchema() error {
	root := ariaRoot()
	reports, err := store.SchemaStatus(root)
	if err != nil {
		return err
	}
	if disk, known, gerr := store.StoreGeneration(root); gerr == nil {
		note := ""
		switch {
		case disk > known:
			note = "  ← written by a newer figaro; this build refuses to open it"
		case disk < known:
			note = "  ← stamped on next open"
		}
		fmt.Printf("%-20s disk=%-4d binary=%-4d%s\n", "store-version", disk, known, note)
	}
	for _, r := range reports {
		disk := fmt.Sprint(r.OnDisk)
		if r.OnDisk == 0 {
			disk = "-"
		}
		note := ""
		switch r.Status {
		case "ahead":
			note = "  ← written by a newer figaro; this build refuses to open it"
		case "behind":
			note = "  ← migrates on next open"
		}
		fmt.Printf("%-20s disk=%-4s binary=%-4d %s%s\n", r.Channel, disk, r.Known, r.Status, note)
	}
	return nil
}

// runDoctorMem asks the running daemon what it is holding: the answer to
// "the daemon is at 3 GB" is which of live agents, cached aria handles,
// sessions or goroutines the number is attached to. It reports the
// daemon's accounting, never this process's.
func runDoctorMem(asJSON bool) error {
	cli, err := angelus.DialClient(transport.UnixEndpoint(angelusSocketPath()))
	if err != nil {
		return fmt.Errorf("no angelus running: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := cli.Status(ctx)
	if err != nil {
		return err
	}
	if st.Mem == nil {
		return fmt.Errorf("the running angelus predates memory accounting; `figaro stop` and retry")
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st.Mem)
	}

	m := st.Mem
	fmt.Printf("arias      live=%d  resident=%d  bound-pids=%d\n",
		m.LiveArias, m.ResidentArias, st.BoundPIDs)
	fmt.Printf("endpoints  open=%d  attached-clients=%d\n", m.Endpoints, m.AttachedClients)
	fmt.Printf("ir cache   resident-rows=%d  resident=%s\n",
		m.ResidentIRRows, humanBytes(int64(m.ResidentIRBytes)))
	fmt.Printf("xlt cache  resident-rows=%d  resident=%s\n",
		m.ResidentTranslationRows, humanBytes(int64(m.ResidentTranslationBytes)))
	fmt.Printf("figwal     loaded-heads=%d  segment-cache=%s of %s  loads=%d\n",
		m.LoadedHeads, humanBytes(m.SegmentCacheBytes),
		humanBytes(m.SegmentCacheBudget), m.SegmentCacheLoads)
	if m.UIWindowBudget > 0 {
		fmt.Printf("ui window  resident=%s of %s  evictions=%d\n",
			humanBytes(m.UIWindowBytes), humanBytes(m.UIWindowBudget), m.UIWindowEvictions)
	}
	if m.Librettos > 0 || m.LibrettoObservers > 0 {
		fmt.Printf("librettos  open=%d  observers=%d  (one fold goroutine each)\n",
			m.Librettos, m.LibrettoObservers)
	}
	if m.LibrettoSweepMinted > 0 || m.LibrettoSweepCorrected > 0 || m.LibrettoSweepMissing > 0 {
		fmt.Printf("           boot sweep: minted=%d corrected=%d still-missing=%d\n",
			m.LibrettoSweepMinted, m.LibrettoSweepCorrected, m.LibrettoSweepMissing)
	}
	fmt.Printf("runtime    goroutines=%d  sessions=%d  gc=%d\n",
		m.Goroutines, m.Sessions, m.NumGC)
	fmt.Printf("heap       alloc=%s  inuse=%s  sys=%s  total-sys=%s\n",
		humanBytes(int64(m.HeapAllocBytes)), humanBytes(int64(m.HeapInuseBytes)),
		humanBytes(int64(m.HeapSysBytes)), humanBytes(int64(m.SysBytes)))

	limit := "unlimited"
	if m.MemLimitBytes != angelus.UnlimitedMemLimit {
		limit = humanBytes(m.MemLimitBytes)
	}
	fmt.Printf("limit      %s (GOMEMLIMIT, soft)\n", limit)

	if m.PprofSocket == "" {
		fmt.Printf("pprof      not armed: restart the daemon with %s=1\n", angelus.PprofEnv)
	} else {
		fmt.Printf("pprof      %s\n", m.PprofSocket)
		fmt.Printf("           go tool pprof -http=: 'http+unix://%s/debug/pprof/heap'\n", m.PprofSocket)
	}

	// A live aria pins its resident handle, so eviction cannot touch it.
	// Saying so beats making the reader remember the rule.
	if m.LiveArias > 0 && m.LiveArias == m.ResidentArias {
		fmt.Printf("\nevery resident aria has a live agent, so idle eviction can free nothing.\n")
	}
	return nil
}

// humanBytes renders a byte count at three significant figures. These are
// read by eye, not parsed: --json exists for the machine.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit && exp < 4; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

// runDoctorSkills prints the first-party skill the binary carries: where it
// unpacked to, and every file in it.
func runDoctorSkills(asJSON bool) error {
	root := outfit.BundledSkillsRoot()
	if root == "" {
		if asJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"bundled": false})
		}
		fmt.Println("bundled skills: disabled (config bundled_skills = false, or FIGARO_BUNDLED_SKILLS)")
		return nil
	}
	dir := filepath.Join(root, "skills")
	files := []string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return fmt.Errorf("bundled skills at %s: %w", root, err)
	}
	slices.Sort(files)

	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"bundled": true, "root": root, "files": files,
		})
	}
	fmt.Printf("bundled skills: %d files, unpacked at\n  %s\n\n", len(files), dir)
	for _, f := range files {
		fmt.Println("  " + f)
	}
	return nil
}

// runDoctorLibrettos recounts every libretto from the boards that name it.
func runDoctorLibrettos(dryRun bool) error {
	if cli, err := angelus.DialClient(transport.UnixEndpoint(angelusSocketPath())); err == nil {
		cli.Close()
		return fmt.Errorf("angelus is running; stop it first (figaro stop)")
	}
	be, err := store.NewXwalBackend(ariaRoot(), 0)
	if err != nil {
		return err
	}
	defer be.Close()

	audit, err := be.AuditLibrettos()
	if err != nil {
		return err
	}
	// Corrected OR Missing: the shortcut used to test only the first, which
	// was written before the pass could MINT anything, so a store whose only
	// problem was a missing libretto was audited and then left alone. Found
	// on the real store, where that is the whole of the migration.
	if !dryRun && (audit.Corrected > 0 || audit.Missing > 0) {
		if audit, err = be.ReconcileLibrettos(); err != nil {
			return err
		}
	}
	verb := "corrected"
	if dryRun {
		verb = "would correct"
	}
	fmt.Printf("boards read      %d\n", audit.Boards)
	fmt.Printf("librettos        %d\n", audit.Librettos)
	fmt.Printf("%-16s %d\n", verb, audit.Corrected)
	if audit.Minted > 0 {
		fmt.Printf("minted           %d  (pre-existing studies migrated)\n", audit.Minted)
	}
	fmt.Printf("orphaned         %d  (no board names them: reclaimable when nothing renders them)\n",
		audit.Orphaned)
	fmt.Printf("%-16s %d  (studied forms still without one%s)\n",
		map[bool]string{true: "would mint", false: "missing"}[dryRun],
		audit.Missing,
		map[bool]string{true: "; run without --dry-run", false: ""}[dryRun])
	return nil
}
