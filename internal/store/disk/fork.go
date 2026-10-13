package disk

import (
	"errors"
	"fmt"
	"github.com/jack-work/figaro/internal/store/segment"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// forkMarkerName records the global log index this fork begins at; the
// parent is always the immediate parent directory.
const forkMarkerName = ".fork"

// forkPendingName signals a fork that crashed before completing: Open
// refuses, and the recovery layer consumes it.
const forkPendingName = ".fork-pending"

const ForkPendingName = forkPendingName

var (
	ErrForkPending     = errors.New("fork in progress: dir contains .fork-pending sentinel")
	ErrForkConflict    = errors.New("fork name conflicts with existing entry")
	ErrInvalidForkName = errors.New("fork name must be a non-empty filename without separators")
)

// readForkMarker returns (0, nil) when .fork is absent: dir is a root.
func readForkMarker(dir string) (uint64, error) {
	path := filepath.Join(dir, forkMarkerName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	// key=value lines; today only base=<N>.
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return 0, fmt.Errorf("malformed .fork line: %q", line)
		}
		switch strings.TrimSpace(k) {
		case "base":
			n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return 0, fmt.Errorf("malformed .fork base: %w", err)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf(".fork present but missing base= field")
}

func writeForkMarker(dir string, base uint64) error {
	final := filepath.Join(dir, forkMarkerName)
	tmp := final + ".tmp"
	body := fmt.Sprintf("base=%d\n", base)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(body)); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	return syncDir(dir)
}

func hasForkPending(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, forkPendingName))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// Subdirectory presence is what marks a log as a branch point and therefore read-only.
func hasSubdirs(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		return true, nil
	}
	return false, nil
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func childSubdirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// validateForkName rejects only the unambiguously broken inputs; no canonicalization.
func validateForkName(name string) error {
	if name == "" || name == "." || name == ".." {
		return ErrInvalidForkName
	}
	if strings.ContainsAny(name, `/\`) {
		return ErrInvalidForkName
	}
	return nil
}

// segAction: keep in the trunk, move whole to the old-future subdir, or
// split into a kept prefix and a moved suffix.
type segAction uint8

const (
	actKeep segAction = iota
	actMove
	actSplit
)

// Fork keeps the prefix [FirstIndex, atIdx-1] and becomes read-only;
// entries at and after atIdx move into a same-named "old future" subdir; a
// fresh subdir childName is created, empty and writable from atIdx.
func (l *Log) Fork(atIdx uint64, childName string, oldFutureNameOpt ...string) (*Log, error) {
	if len(oldFutureNameOpt) > 1 {
		return nil, fmt.Errorf("Fork: at most one oldFutureName override permitted, got %d",
			len(oldFutureNameOpt))
	}
	oldFutureName := ""
	if len(oldFutureNameOpt) == 1 {
		oldFutureName = oldFutureNameOpt[0]
	}
	// On a re-split all existing child forks move into the old-future.
	return l.forkImpl(atIdx, childName, oldFutureName, oldFutureName != "", nil, true)
}

// ForkRehome takes an explicit list of child subdirs to re-home into the
// old-future, so the joint fork re-homes the same children in every channel,
// even where a sparse related channel would tail-fork. An empty non-nil list
// re-homes nothing.
func (l *Log) ForkRehome(atIdx uint64, childName, oldFutureName string, rehome []string) (*Log, error) {
	return l.forkImpl(atIdx, childName, oldFutureName, oldFutureName != "", rehome, false)
}

// ChildForkBases is each child fork subdir's declared .fork base.
func (l *Log) ChildForkBases() (map[string]uint64, error) {
	kids, err := childSubdirs(l.dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]uint64, len(kids))
	for _, name := range kids {
		base, err := readForkMarker(filepath.Join(l.dir, name))
		if err != nil {
			return nil, err
		}
		out[name] = base
	}
	return out, nil
}

func (l *Log) forkImpl(atIdx uint64, childName, oldFutureName string, oldFutureExplicit bool, rehome []string, autoRehome bool) (*Log, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := validateForkName(childName); err != nil {
		return nil, err
	}
	if oldFutureName == "" {
		oldFutureName = filepath.Base(l.dir)
	} else if err := validateForkName(oldFutureName); err != nil {
		return nil, fmt.Errorf("oldFutureName: %w", err)
	}
	if childName == oldFutureName {
		return nil, fmt.Errorf("%w: childName %q equals old-future subdir name",
			ErrForkConflict, childName)
	}
	empty := l.isEmptyLocked()
	first := l.firstIndexLocked()
	last := l.lastIndexLocked()
	if empty {
		want := l.forkBase
		if want == 0 {
			want = 1
		}
		if atIdx != want {
			return nil, fmt.Errorf("fork index %d invalid for empty log (want %d)", atIdx, want)
		}
	} else {
		// atIdx must leave a non-empty prefix, except that a forked node may
		// fork at its own first index: all of its own entries move to the
		// old-future and reads below forkBase resolve through the parent.
		lowOK := atIdx > first || (l.forkBase > 0 && atIdx == first)
		if !lowOK || atIdx > last+1 {
			return nil, fmt.Errorf("fork index %d out of range (%d, %d]",
				atIdx, first, last+1)
		}
	}
	// childName must not collide with a segment file or a sibling fork.
	if p := filepath.Join(l.dir, childName); pathExists(p) {
		return nil, fmt.Errorf("%w: %s", ErrForkConflict, p)
	}
	// Captured before any new subdir is created.
	existingChildren, err := childSubdirs(l.dir)
	if err != nil {
		return nil, err
	}

	// Seal the active segment so the plan only considers sealed segments.
	if l.active != nil {
		if err := l.active.Sync(); err != nil {
			return nil, err
		}
		if l.active.Count() > 0 {
			l.sealed = append(l.sealed, wrapOpen(l.active))
			l.active = nil
		}
	}

	// A fork reasons about the whole log, so every sealed segment is opened
	// before the plan is made.
	if err := l.materializeLocked(); err != nil {
		return nil, err
	}
	segs := l.sealedSegments()

	// Header mode: the new branches need a fresh watermark at the fork
	// boundary (state at atIdx-1), since the prefix is read-only. Computed
	// once here, while the log is intact.
	var forkWatermark []byte
	headerMode := l.opts.OnSegmentOpen != nil
	if headerMode {
		var w []byte
		var werr error
		if empty {
			if l.active != nil {
				w = append([]byte(nil), l.active.Header()...)
			} else if l.parent != nil && atIdx > 1 {
				w, werr = l.parent.StateAt(atIdx - 1)
			} else {
				w, werr = l.opts.OnSegmentOpen(nil, nil)
			}
		} else {
			w, werr = l.stateAtLocked(atIdx - 1)
		}
		if werr != nil {
			return nil, fmt.Errorf("fork watermark at %d: %w", atIdx-1, werr)
		}
		forkWatermark = w
	}

	// Classify each sealed segment relative to atIdx.
	actions := make([]segAction, len(l.sealed))
	hasMoves := false
	hasSplit := false
	for i, s := range l.sealed {
		switch {
		case s.LastIndex() < atIdx:
			actions[i] = actKeep
		case s.BaseIndex() >= atIdx:
			actions[i] = actMove
			hasMoves = true
		default:
			actions[i] = actSplit
			hasSplit = true
		}
	}
	// dataMoves: the node's own segments carry a suffix into the old-future.
	// The old-future branch is created whenever explicitly named (so the
	// continuation gets a branch in every channel), else only with data.
	dataMoves := hasMoves || hasSplit
	// autoRehome: all existing children when the node's own segments split;
	// otherwise the explicit list, filtered to ones that exist here.
	var rehomeChildren []string
	if autoRehome {
		if dataMoves {
			rehomeChildren = existingChildren
		}
	} else {
		for _, name := range rehome {
			if pathExists(filepath.Join(l.dir, name)) {
				rehomeChildren = append(rehomeChildren, name)
			}
		}
	}
	createOldFuture := oldFutureExplicit || dataMoves || len(rehomeChildren) > 0
	if createOldFuture {
		if p := filepath.Join(l.dir, oldFutureName); pathExists(p) {
			return nil, fmt.Errorf("%w: %s", ErrForkConflict, p)
		}
	}
	oldFutureDir := filepath.Join(l.dir, oldFutureName)
	newForkDir := filepath.Join(l.dir, childName)

	// Write the sentinel describing the plan; remove it on success.
	pendingPath := filepath.Join(l.dir, forkPendingName)
	pendingBody := fmt.Sprintf("at=%d\nchild=%s\nold=%s\n",
		atIdx, childName, oldFutureName)
	if err := os.WriteFile(pendingPath, []byte(pendingBody), 0644); err != nil {
		return nil, err
	}
	if err := syncDir(l.dir); err != nil {
		_ = os.Remove(pendingPath)
		return nil, err
	}
	rollbackPending := func(err error) error {
		// Best-effort: clear the sentinel even on failure; half-staged files
		// remain for the operator.
		_ = os.Remove(pendingPath)
		return err
	}

	if createOldFuture {
		if err := os.MkdirAll(oldFutureDir, 0755); err != nil {
			return nil, rollbackPending(err)
		}
	}
	if err := os.MkdirAll(newForkDir, 0755); err != nil {
		return nil, rollbackPending(err)
	}

	// Header mode: seed the child with a header-only first segment carrying
	// the fork watermark, so its first append builds on the fork-point state.
	writeWatermarkSeg := func(dir string) error {
		seg, err := segment.Create(filepath.Join(dir, l.segName(atIdx)), l.codec, atIdx, l.opts.SegmentSize)
		if err != nil {
			return err
		}
		if err := seg.WriteHeader(forkWatermark); err != nil {
			seg.Close()
			return err
		}
		if err := seg.Sync(); err != nil {
			seg.Close()
			return err
		}
		return seg.Close()
	}
	if headerMode {
		if err := writeWatermarkSeg(newForkDir); err != nil {
			return nil, rollbackPending(err)
		}
		oldFutureHasBoundary := hasSplit
		if !oldFutureHasBoundary {
			for i, action := range actions {
				if action == actMove && l.sealed[i].BaseIndex() == atIdx {
					oldFutureHasBoundary = true
					break
				}
			}
		}
		if createOldFuture && !oldFutureHasBoundary {
			if err := writeWatermarkSeg(oldFutureDir); err != nil {
				return nil, rollbackPending(err)
			}
		}
	}

	// Split the boundary segment if one exists.
	var prefixReplacement *segment.Segment
	for i := range l.sealed {
		if actions[i] != actSplit {
			continue
		}
		s := segs[i]
		prefixPath := s.Path()
		prefixTmp := prefixPath + ".tmp"
		suffixPath := filepath.Join(oldFutureDir, l.segName(atIdx))

		prefix, err := segment.Create(prefixTmp, l.codec, s.BaseIndex(), 0)
		if err != nil {
			return nil, rollbackPending(err)
		}
		// The prefix keeps the boundary segment's original watermark; the
		// suffix begins from the fork watermark.
		if headerMode {
			if err := prefix.WriteHeader(s.Header()); err != nil {
				prefix.Close()
				return nil, rollbackPending(err)
			}
		}
		boundaryLocalCut := atIdx - s.BaseIndex()
		for j := uint64(0); j < boundaryLocalCut; j++ {
			payload, rerr := s.ReadIndex(j)
			if rerr != nil {
				prefix.Close()
				return nil, rollbackPending(rerr)
			}
			if _, aerr := prefix.Append(payload); aerr != nil {
				prefix.Close()
				return nil, rollbackPending(aerr)
			}
		}
		if err := prefix.Sync(); err != nil {
			prefix.Close()
			return nil, rollbackPending(err)
		}
		prefix.Close()

		suffix, err := segment.Create(suffixPath, l.codec, atIdx, 0)
		if err != nil {
			return nil, rollbackPending(err)
		}
		if headerMode {
			if err := suffix.WriteHeader(forkWatermark); err != nil {
				suffix.Close()
				return nil, rollbackPending(err)
			}
		}
		for j := boundaryLocalCut; j < s.Count(); j++ {
			payload, rerr := s.ReadIndex(j)
			if rerr != nil {
				suffix.Close()
				return nil, rollbackPending(rerr)
			}
			if _, aerr := suffix.Append(payload); aerr != nil {
				suffix.Close()
				return nil, rollbackPending(aerr)
			}
		}
		if err := suffix.Sync(); err != nil {
			suffix.Close()
			return nil, rollbackPending(err)
		}
		suffix.Close()

		// Replace the original boundary file with the prefix.
		if err := s.Close(); err != nil {
			return nil, rollbackPending(err)
		}
		if err := os.Rename(prefixTmp, prefixPath); err != nil {
			return nil, rollbackPending(err)
		}
		// The reopened prefix stays in this Log's sealed list.
		var reopen *segment.Segment
		if headerMode {
			reopen, err = segment.OpenReadOnlyHeadered(prefixPath, l.codec, s.BaseIndex())
		} else {
			reopen, err = segment.OpenReadOnly(prefixPath, l.codec, s.BaseIndex())
		}
		if err != nil {
			return nil, rollbackPending(err)
		}
		prefixReplacement = reopen
		break
	}

	// Move whole-move segments into the old-future subdir.
	for i := range l.sealed {
		if actions[i] != actMove {
			continue
		}
		s := segs[i]
		src := s.Path()
		dst := filepath.Join(oldFutureDir, filepath.Base(src))
		if err := s.Close(); err != nil {
			return nil, rollbackPending(err)
		}
		if err := os.Rename(src, dst); err != nil {
			return nil, rollbackPending(err)
		}
	}

	// Re-homed children branched from the suffix, which now lives in the
	// old-future; their own .fork base is unchanged and their parent resolves
	// via `..`.
	for _, name := range rehomeChildren {
		src := filepath.Join(l.dir, name)
		dst := filepath.Join(oldFutureDir, name)
		if err := os.Rename(src, dst); err != nil {
			return nil, rollbackPending(err)
		}
	}

	// The old-future's declared base equals atIdx.
	if createOldFuture {
		if err := writeForkMarker(oldFutureDir, atIdx); err != nil {
			return nil, rollbackPending(err)
		}
	}
	if err := writeForkMarker(newForkDir, atIdx); err != nil {
		return nil, rollbackPending(err)
	}

	// fsync every directory we touched so dentries are durable.
	if err := syncDir(l.dir); err != nil {
		return nil, rollbackPending(err)
	}
	if createOldFuture {
		if err := syncDir(oldFutureDir); err != nil {
			return nil, rollbackPending(err)
		}
	}
	if err := syncDir(newForkDir); err != nil {
		return nil, rollbackPending(err)
	}

	// Keep the kept segments and the split prefix, which slots in where the
	// boundary used to be.
	newSealed := make([]*sealedSeg, 0, len(l.sealed))
	for i, ss := range l.sealed {
		switch actions[i] {
		case actKeep:
			newSealed = append(newSealed, ss)
		case actSplit:
			if prefixReplacement != nil {
				newSealed = append(newSealed, wrapOpen(prefixReplacement))
			}
		}
	}
	l.sealed = newSealed
	if l.active != nil {
		if err := l.active.Close(); err != nil {
			return nil, rollbackPending(err)
		}
	}
	l.active = nil
	l.readOnly = true

	if err := os.Remove(pendingPath); err != nil {
		return nil, err
	}
	if err := syncDir(l.dir); err != nil {
		return nil, err
	}
	slog.Info("log forked",
		"dir", l.dir,
		"atIdx", atIdx,
		"child", childName,
		"oldFuture", createOldFuture)

	// Open the new fork as a child of this log.
	childOpts := l.opts
	childOpts.Parent = l
	return Open(newForkDir, childOpts)
}
