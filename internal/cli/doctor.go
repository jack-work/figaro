package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/outfit"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
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
	if cli, err := sdk.DialAngelus(transport.UnixEndpoint(angelusSocketPath())); err == nil {
		cli.Close()
		return fmt.Errorf("angelus is running; stop it first (figaro stop)")
	}
	root := ariaRoot()
	manPath := filepath.Join(root, "xwal.json")
	raw, err := os.ReadFile(manPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(stdout, "no store; nothing to do")
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
		fmt.Fprintln(stdout, "store clean; nothing to do")
		return nil
	}

	var freed int64
	for _, name := range dead {
		dir := filepath.Join(root, filepath.FromSlash(name))
		freed += dirSize(dir)
	}
	if dryRun {
		fmt.Fprintf(stdout, "would remove %d dead channel(s) (%s): %s\n", len(dead), tool.FormatSize(int(freed)), strings.Join(dead, ", "))
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
	fmt.Fprintf(stdout, "removed %d dead channel(s), freed %s: %s\n", len(dead), tool.FormatSize(int(freed)), strings.Join(dead, ", "))
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
		fmt.Fprintf(stdout, "%-20s disk=%-4d binary=%-4d%s\n", "store-version", disk, known, note)
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
		fmt.Fprintf(stdout, "%-20s disk=%-4s binary=%-4d %s%s\n", r.Channel, disk, r.Known, r.Status, note)
	}
	return nil
}

// runDoctorMem asks the running daemon what it is holding: the answer to
// "the daemon is at 3 GB" is which of live agents, cached aria handles,
// sessions or goroutines the number is attached to. It reports the
// daemon's accounting, never this process's.
func runDoctorMem(asJSON, collect bool) error {
	cli, err := sdk.DialAngelus(transport.UnixEndpoint(angelusSocketPath()))
	if err != nil {
		return fmt.Errorf("no angelus running: %w", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := cli.Status(ctx)
	if collect {
		st, err = cli.MemCollect(ctx)
		if err != nil && strings.Contains(err.Error(), "method not found") {
			// A daemon older than this flag answers -32601, which tells the
			// operator nothing. Name the actual remedy.
			return fmt.Errorf("the running angelus predates `doctor mem --gc`; `figaro stop` and retry")
		}
	}
	if err != nil {
		return err
	}
	if st.Mem == nil {
		return fmt.Errorf("the running angelus predates memory accounting; `figaro stop` and retry")
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st.Mem)
	}

	m := st.Mem
	fmt.Fprintf(stdout, "arias      live=%d  resident=%d  bound-pids=%d\n",
		m.LiveArias, m.ResidentArias, st.BoundPIDs)
	fmt.Fprintf(stdout, "endpoints  open=%d  attached-clients=%d\n", m.Endpoints, m.AttachedClients)
	fmt.Fprintf(stdout, "ir cache   resident-rows=%d  resident=%s\n",
		m.ResidentIRRows, humanBytes(int64(m.ResidentIRBytes)))
	fmt.Fprintf(stdout, "xlt cache  resident-rows=%d  resident=%s\n",
		m.ResidentTranslationRows, humanBytes(int64(m.ResidentTranslationBytes)))
	fmt.Fprintf(stdout, "figwal     loaded-heads=%d  segment-cache=%s of %s  loads=%d\n",
		m.LoadedHeads, humanBytes(m.SegmentCacheBytes),
		humanBytes(m.SegmentCacheBudget), m.SegmentCacheLoads)
	if m.UIWindowBudget > 0 {
		fmt.Fprintf(stdout, "ui window  resident=%s of %s  evictions=%d\n",
			humanBytes(m.UIWindowBytes), humanBytes(m.UIWindowBudget), m.UIWindowEvictions)
	}
	if m.Librettos > 0 || m.LibrettoObservers > 0 {
		fmt.Fprintf(stdout, "librettos  open=%d  observers=%d  (one fold goroutine each)\n",
			m.Librettos, m.LibrettoObservers)
	}
	if m.LibrettoSweepMinted > 0 || m.LibrettoSweepCorrected > 0 || m.LibrettoSweepMissing > 0 {
		fmt.Fprintf(stdout, "           boot sweep: minted=%d corrected=%d still-missing=%d\n",
			m.LibrettoSweepMinted, m.LibrettoSweepCorrected, m.LibrettoSweepMissing)
	}
	fmt.Fprintf(stdout, "runtime    goroutines=%d  sessions=%d  gc=%d\n",
		m.Goroutines, m.Sessions, m.NumGC)
	if c := m.Collected; c != nil {
		fmt.Fprintf(stdout, "collected  live %s -> %s  (reclaimed %s in %s)\n",
			humanBytes(int64(c.BeforeBytes)), humanBytes(int64(c.AfterBytes)),
			humanBytes(int64(c.ReclaimedByes)), c.Took.Round(time.Millisecond))
		fmt.Fprintf(stdout, "           the AFTER figure is the live set; the before was allocation not yet swept\n")
	}
	fmt.Fprintf(stdout, "heap       alloc=%s  inuse=%s  sys=%s  total-sys=%s\n",
		humanBytes(int64(m.HeapAllocBytes)), humanBytes(int64(m.HeapInuseBytes)),
		humanBytes(int64(m.HeapSysBytes)), humanBytes(int64(m.SysBytes)))
	// WHAT THE OPERATING SYSTEM SEES, spelled out, because RSS next to the
	// caches above reads as a leak when it is usually arithmetic: idle spans
	// the scavenger has not yet handed back are resident and are not held by
	// anything.
	//
	// A LIVE GO HEAP ALWAYS HAS SOME IDLE SPAN. Zero here means the daemon
	// predates the field, and printing a measured-looking 0 would be worse
	// than printing nothing.
	if m.HeapIdleBytes == 0 && m.HeapSysBytes > 0 {
		fmt.Fprintf(stdout, "           (this angelus predates idle/released accounting; `figaro stop` to refresh)\n")
	} else {
		fmt.Fprintf(stdout, "           idle=%s  released-to-os=%s  resident-heap~=%s\n",
			humanBytes(int64(m.HeapIdleBytes)), humanBytes(int64(m.HeapReleasedBytes)),
			humanBytes(int64(m.HeapSysBytes-m.HeapReleasedBytes)))
	}
	if m.Collected == nil {
		fmt.Fprintf(stdout, "           alloc counts garbage not yet swept; `doctor mem --gc` for the live set\n")
	}

	limit := "unlimited"
	if m.MemLimitBytes != angelus.UnlimitedMemLimit {
		limit = humanBytes(m.MemLimitBytes)
	}
	fmt.Fprintf(stdout, "limit      %s (GOMEMLIMIT, soft)\n", limit)

	if m.PprofSocket == "" {
		fmt.Fprintf(stdout, "pprof      not armed: restart the daemon with %s=1\n", angelus.PprofEnv)
	} else {
		fmt.Fprintf(stdout, "pprof      %s\n", m.PprofSocket)
		fmt.Fprintf(stdout, "           go tool pprof -http=: 'http+unix://%s/debug/pprof/heap'\n", m.PprofSocket)
	}

	// A live aria pins its resident handle, so eviction cannot touch it.
	// Saying so beats making the reader remember the rule.
	if m.LiveArias > 0 && m.LiveArias == m.ResidentArias {
		fmt.Fprintf(stdout, "\nevery resident aria has a live agent, so idle eviction can free nothing.\n")
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
			return json.NewEncoder(stdout).Encode(map[string]any{"bundled": false})
		}
		fmt.Fprintln(stdout, "bundled skills: disabled (config bundled_skills = false, or FIGARO_BUNDLED_SKILLS)")
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
		return json.NewEncoder(stdout).Encode(map[string]any{
			"bundled": true, "root": root, "files": files,
		})
	}
	fmt.Fprintf(stdout, "bundled skills: %d files, unpacked at\n  %s\n\n", len(files), dir)
	for _, f := range files {
		fmt.Fprintln(stdout, "  "+f)
	}
	return nil
}

// runDoctorLibrettos recounts every libretto from the boards that name it.
func runDoctorLibrettos(dryRun bool) error {
	if cli, err := sdk.DialAngelus(transport.UnixEndpoint(angelusSocketPath())); err == nil {
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
	fmt.Fprintf(stdout, "boards read      %d\n", audit.Boards)
	fmt.Fprintf(stdout, "librettos        %d\n", audit.Librettos)
	fmt.Fprintf(stdout, "%-16s %d\n", verb, audit.Corrected)
	if audit.Minted > 0 {
		fmt.Fprintf(stdout, "minted           %d  (pre-existing studies migrated)\n", audit.Minted)
	}
	fmt.Fprintf(stdout, "orphaned         %d  (no board names them: reclaimable when nothing renders them)\n",
		audit.Orphaned)
	fmt.Fprintf(stdout, "%-16s %d  (studied forms still without one%s)\n",
		map[bool]string{true: "would mint", false: "missing"}[dryRun],
		audit.Missing,
		map[bool]string{true: "; run without --dry-run", false: ""}[dryRun])
	return nil
}

// runDoctorToolCalls closes tool invokes that no result ever answered.
//
// The door keeps this from happening going forward, so what this finds is
// history written by a figaro older than the door. An aria carrying one is
// refused by every provider on every send -- "tool_use ids were found without
// tool_result blocks" -- and cannot be used again until it is closed.
func runDoctorToolCalls(dryRun bool) error {
	if cli, err := sdk.DialAngelus(transport.UnixEndpoint(angelusSocketPath())); err == nil {
		cli.Close()
		return fmt.Errorf("angelus is running; stop it first (figaro stop)")
	}
	be, err := store.NewXwalBackend(ariaRoot(), 0)
	if err != nil {
		return err
	}
	defer be.Close()

	arias, repaired := 0, 0
	for _, cv := range be.Conversations() {
		n, err := be.UnmatchedToolCalls(cv.ID)
		if err != nil || n == 0 {
			continue
		}
		arias++
		fmt.Fprintf(stdout, "%-10s %d unanswered tool call(s)\n", cv.ID, n)
		if dryRun {
			continue
		}
		if fixed, err := be.RepairToolCalls(cv.ID); err != nil {
			fmt.Fprintf(stderrw, "  %s: %v\n", cv.ID, err)
		} else {
			repaired += fixed
		}
	}
	switch {
	case arias == 0:
		fmt.Fprintln(stdout, "no aria carries an unanswered tool call")
	case dryRun:
		fmt.Fprintf(stdout, "\n%d aria(s) would be repaired; run without --dry-run\n", arias)
	default:
		fmt.Fprintf(stdout, "\nclosed %d call(s) across %d aria(s)\n", repaired, arias)
	}
	return nil
}
