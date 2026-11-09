package crashtest

import (
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Only files written during the current child run are corruption targets:
// a real power loss tears unsynced data, and everything older was already
// on disk before the cycle started.
func dirtySegments(dir string, cutoff time.Time) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.Size() > 0 && !fi.ModTime().Before(cutoff) {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

func corruptTail(rng *rand.Rand, dir string, cutoff time.Time) ([]string, error) {
	segs, err := dirtySegments(dir, cutoff)
	if err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		return nil, nil
	}
	rng.Shuffle(len(segs), func(i, j int) { segs[i], segs[j] = segs[j], segs[i] })
	n := 1 + rng.Intn(2)
	if n > len(segs) {
		n = len(segs)
	}
	var hit []string
	for _, seg := range segs[:n] {
		kind, err := corruptFile(rng, seg)
		if err != nil {
			return hit, err
		}
		hit = append(hit, kind+" "+seg)
	}
	return hit, nil
}

func corruptFile(rng *rand.Rand, path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	size := fi.Size()
	bound := func(max int64) int64 {
		if max > size {
			max = size
		}
		return 1 + rng.Int63n(max)
	}
	switch rng.Intn(4) {
	case 0:
		return "truncate", os.Truncate(path, size-bound(250))
	case 1:
		n := bound(120)
		garbage := make([]byte, n)
		rng.Read(garbage)
		return "overwrite", writeAt(path, size-n, garbage)
	case 2:
		garbage := make([]byte, 1+rng.Intn(80))
		rng.Read(garbage)
		return "append", writeAt(path, size, garbage)
	default:
		n := bound(200)
		buf := make([]byte, n)
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return "", err
		}
		defer f.Close()
		if _, err := f.ReadAt(buf, size-n); err != nil {
			return "", err
		}
		for i := 0; i < 1+rng.Intn(6); i++ {
			pos := rng.Intn(len(buf))
			if buf[pos] >= '0' && buf[pos] <= '9' {
				buf[pos] = byte('0' + rng.Intn(10))
			} else {
				buf[pos] = byte('a' + rng.Intn(26))
			}
		}
		_, err = f.WriteAt(buf, size-n)
		return "flip", err
	}
}

func writeAt(path string, off int64, b []byte) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteAt(b, off); err != nil {
		return err
	}
	return f.Close()
}
