package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/figwal/disk"
)

const (
	KeyCheckpointMarks        = "system.checkpoint.marks"
	KeyCheckpointEveryPatches = "system.checkpoint.every_patches"
	KeyCheckpointEverySeconds = "system.checkpoint.every_seconds"
	KeyCheckpointMaxMarks     = "system.checkpoint.max_marks"

	PinTail = "tail"

	KeyRetention        = "system.retention"
	KeyRetentionMode    = "system.retention.mode"
	KeyRetentionWriters = "system.retention.writers"

	RetentionMarks = "marks"
	RetentionNone  = "none"
)

var ErrRetentionOnConversation = errors.New("system.retention: a conversation-bound form keeps every record (its old turns are retranslated from them)")

func touchesRetention(p form.Patch) bool {
	for _, e := range p.Entries() {
		if e.Key == KeyRetention || strings.HasPrefix(e.Key, KeyRetention+".") {
			return true
		}
	}
	return false
}

func RetentionModeOf(s form.Snapshot) string {
	raw, ok := s.Get(KeyRetentionMode)
	if !ok {
		return ""
	}
	var mode string
	if json.Unmarshal(raw, &mode) != nil {
		return ""
	}
	return mode
}

func RetentionWritersOf(s form.Snapshot) []string {
	raw, ok := s.Get(KeyRetentionWriters)
	if !ok {
		return nil
	}
	var ws []string
	if json.Unmarshal(raw, &ws) != nil {
		return nil
	}
	return ws
}

const (
	minCheckpointEveryPatches = 64
	minCheckpointEverySeconds = 5
	minCheckpointMarks        = 2
)

var (
	checkpointEveryPatches atomic.Int64
	checkpointEverySeconds atomic.Int64
	checkpointMaxMarks     atomic.Int64
)

func init() { checkpointMaxMarks.Store(5) }

func SetCheckpointDefaults(everyPatches, everySeconds, maxMarks int) {
	checkpointEveryPatches.Store(int64(everyPatches))
	checkpointEverySeconds.Store(int64(everySeconds))
	checkpointMaxMarks.Store(int64(maxMarks))
}

func CheckpointDefaultsForTest() (int, int, int) {
	return int(checkpointEveryPatches.Load()), int(checkpointEverySeconds.Load()), int(checkpointMaxMarks.Load())
}

type checkpointPolicy struct {
	everyPatches uint64
	every        time.Duration
	maxMarks     int
}

func (p checkpointPolicy) off() bool { return p.everyPatches == 0 && p.every == 0 }

func checkpointPolicyOf(s form.Snapshot) checkpointPolicy {
	n := settingOr(s, KeyCheckpointEveryPatches, checkpointEveryPatches.Load())
	secs := settingOr(s, KeyCheckpointEverySeconds, checkpointEverySeconds.Load())
	marks := settingOr(s, KeyCheckpointMaxMarks, checkpointMaxMarks.Load())
	p := checkpointPolicy{maxMarks: int(max(marks, minCheckpointMarks))}
	if n > 0 {
		p.everyPatches = uint64(max(n, minCheckpointEveryPatches))
	}
	if secs > 0 {
		p.every = time.Duration(max(secs, minCheckpointEverySeconds)) * time.Second
	}
	return p
}

func settingOr(s form.Snapshot, key string, def int64) int64 {
	raw, ok := s.Get(key)
	if !ok {
		return def
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return def
	}
	return n
}

func (p checkpointPolicy) due(head, last uint64, lastAt, now time.Time) bool {
	if p.off() || head <= last {
		return false
	}
	if p.everyPatches > 0 && head-last >= p.everyPatches {
		return true
	}
	return p.every > 0 && !lastAt.IsZero() && now.Sub(lastAt) >= p.every
}

type Mark struct {
	Pin  string `json:"pin,omitempty"`
	Used int64  `json:"used"`
}

func MarksOf(s form.Snapshot) map[uint64]Mark {
	raw, ok := s.Get(KeyCheckpointMarks)
	if !ok {
		return nil
	}
	var byKey map[string]Mark
	if err := json.Unmarshal(raw, &byKey); err != nil {
		return nil
	}
	out := make(map[uint64]Mark, len(byKey))
	for k, m := range byKey {
		if v, err := strconv.ParseUint(k, 10, 64); err == nil {
			out[v] = m
		}
	}
	return out
}

func marksPatch(base form.Snapshot, marks map[uint64]Mark) (form.Patch, error) {
	byKey := make(map[string]Mark, len(marks))
	for v, m := range marks {
		byKey[strconv.FormatUint(v, 10)] = m
	}
	raw, err := json.Marshal(byKey)
	if err != nil {
		return form.Patch{}, err
	}
	return form.Build(base, map[string]json.RawMessage{KeyCheckpointMarks: raw}, nil), nil
}

func evictMarks(marks map[uint64]Mark, maxMarks int) []uint64 {
	var out []uint64
	for len(marks) > maxMarks {
		var victim uint64
		found := false
		for _, v := range slices.Sorted(maps.Keys(marks)) {
			m := marks[v]
			if m.Pin == PinTail {
				continue
			}
			if !found || m.Used < marks[victim].Used {
				victim, found = v, true
			}
		}
		if !found {
			break
		}
		delete(marks, victim)
		out = append(out, victim)
	}
	return out
}

func LowestMark(marks map[uint64]Mark) uint64 {
	if len(marks) == 0 {
		return 0
	}
	return slices.Min(slices.Collect(maps.Keys(marks)))
}

type formCheckpoint struct {
	Version   uint64 `json:"version"`
	RecordCRC uint32 `json:"record_crc"`
	BodyCRC   uint32 `json:"body_crc"`
	BodyLen   int    `json:"body_len"`
	body      []byte
}

type checkpointLog interface {
	RecordCRC(version uint64) (uint32, error)
	WriteCheckpoint(version uint64, recordCRC uint32, body []byte) error
	CheckpointVersions() []uint64
	ReadCheckpoint(version uint64) (formCheckpoint, error)
	RemoveCheckpoint(version uint64) error
}

const checkpointMagic = "FIGCKPT1\n"

var errCheckpointCorrupt = errors.New("checkpoint: corrupt")

func checkpointName(v uint64) string { return fmt.Sprintf("%020d.ckpt", v) }

func checkpointDirOf(root, id string) string {
	return filepath.Join(root, "checkpoints", url.PathEscape(id))
}

func writeCheckpointFile(dir string, v uint64, recordCRC uint32, body []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	head, err := json.Marshal(formCheckpoint{Version: v, RecordCRC: recordCRC, BodyCRC: crc32.ChecksumIEEE(body), BodyLen: len(body)})
	if err != nil {
		return err
	}
	final := filepath.Join(dir, checkpointName(v))
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	w.WriteString(checkpointMagic)
	w.Write(head)
	w.WriteByte('\n')
	w.Write(body)
	err = errors.Join(w.Flush(), f.Sync(), f.Close())
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return err
	}
	return disk.SyncDir(dir)
}

func readCheckpointFile(path string) (formCheckpoint, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return formCheckpoint{}, err
	}
	rest, ok := bytes.CutPrefix(raw, []byte(checkpointMagic))
	if !ok {
		return formCheckpoint{}, errCheckpointCorrupt
	}
	head, body, ok := bytes.Cut(rest, []byte{'\n'})
	if !ok {
		return formCheckpoint{}, errCheckpointCorrupt
	}
	var c formCheckpoint
	if err := json.Unmarshal(head, &c); err != nil {
		return formCheckpoint{}, errCheckpointCorrupt
	}
	if len(body) != c.BodyLen || crc32.ChecksumIEEE(body) != c.BodyCRC {
		return formCheckpoint{}, errCheckpointCorrupt
	}
	c.body = body
	return c, nil
}

func checkpointVersions(dir string) []uint64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []uint64
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".ckpt")
		if !ok {
			continue
		}
		if v, err := strconv.ParseUint(name, 10, 64); err == nil {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

func removeCheckpointFile(dir string, v uint64) error {
	err := os.Remove(filepath.Join(dir, checkpointName(v)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func newestCheckpoint(dir string, last uint64, recordCRC func(uint64) (uint32, error)) (formCheckpoint, bool) {
	vs := checkpointVersions(dir)
	for i := len(vs) - 1; i >= 0; i-- {
		v := vs[i]
		if v > last {
			continue
		}
		c, err := readCheckpointFile(filepath.Join(dir, checkpointName(v)))
		if err != nil {
			slog.Warn("checkpoint unreadable", "dir", dir, "version", v, "err", err)
			continue
		}
		got, err := recordCRC(v)
		if err != nil || got != c.RecordCRC || c.Version != v {
			continue
		}
		return c, true
	}
	return formCheckpoint{}, false
}

func payloadCRC(p []byte) uint32 { return crc32.ChecksumIEEE(p) }
