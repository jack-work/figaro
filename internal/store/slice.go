package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/store/xwal"
)

type SliceOptions struct {
	Arias    int
	MaxBytes int64
}

type SliceReport struct {
	Arias      int
	Candidates int
	Skipped    int
	Nodes      int
	Bytes      int64
	Elapsed    time.Duration
}

type sliceNode struct {
	bytes  int64
	active time.Time
}

const metaDirName = "_meta"

func CopySlice(src, dst string, opt SliceOptions) (SliceReport, error) {
	start := time.Now()
	layout, err := xwal.ReadLayout(src)
	if err != nil {
		return SliceReport{}, err
	}
	if _, err := os.Stat(dst); err == nil {
		return SliceReport{}, fmt.Errorf("slice: %s already exists", dst)
	}
	nodes := map[string]sliceNode{}
	for key, info := range layout.Nodes {
		n := sliceNode{}
		dir := xwal.NodeDirName(key)
		for _, ch := range layout.Channels {
			b, t := flatDirUsage(filepath.Join(src, ch, dir))
			n.bytes += b
			if t.After(n.active) {
				n.active = t
			}
		}
		if info.Trunk != "" {
			if fi, err := os.Stat(filepath.Join(src, metaDirName, info.Trunk+".json")); err == nil {
				n.bytes += fi.Size()
				if fi.ModTime().After(n.active) {
					n.active = fi.ModTime()
				}
			}
		}
		nodes[key] = n
	}

	kept := map[string]bool{}
	var total int64
	take := func(keys []string) {
		for _, k := range keys {
			if !kept[k] {
				kept[k] = true
				total += nodes[k].bytes
			}
		}
	}
	cost := func(keys []string) int64 {
		var c int64
		for _, k := range keys {
			if !kept[k] {
				c += nodes[k].bytes
			}
		}
		return c
	}
	var arias []string
	for key, info := range layout.Nodes {
		if info.Kind == string(kindConversation) && info.Trunk != "" {
			arias = append(arias, key)
			continue
		}
		if info.Kind != string(kindConversation) {
			take(layout.Lineage(key))
		}
	}
	sort.Slice(arias, func(i, j int) bool {
		a, b := nodes[arias[i]].active, nodes[arias[j]].active
		if !a.Equal(b) {
			return a.After(b)
		}
		return arias[i] < arias[j]
	})
	rep := SliceReport{Candidates: len(arias)}
	for _, key := range arias {
		if opt.Arias > 0 && rep.Arias >= opt.Arias {
			break
		}
		lineage := layout.Lineage(key)
		if opt.MaxBytes > 0 && total+cost(lineage) > opt.MaxBytes {
			rep.Skipped++
			continue
		}
		take(lineage)
		rep.Arias++
	}

	trunks := map[string]bool{}
	for key := range kept {
		if t := layout.Nodes[key].Trunk; t != "" {
			trunks[t] = true
		}
	}
	if err := copySliceTree(src, dst, layout, kept, trunks); err != nil {
		os.RemoveAll(dst)
		return SliceReport{}, err
	}
	rep.Nodes = len(kept)
	rep.Bytes = total
	rep.Elapsed = time.Since(start)
	return rep, nil
}

func flatDirUsage(dir string) (int64, time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, time.Time{}
	}
	var size int64
	var newest time.Time
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		size += fi.Size()
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return size, newest
}

func copySliceTree(src, dst string, layout xwal.Layout, kept, trunks map[string]bool) error {
	channels := map[string]bool{}
	for _, ch := range layout.Channels {
		channels[ch] = true
	}
	keptDirs := map[string]bool{}
	for key := range kept {
		keptDirs[xwal.NodeDirName(key)] = true
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		parent := filepath.ToSlash(filepath.Dir(rel))
		if d.IsDir() && channels[parent] && !keptDirs[d.Name()] {
			return filepath.SkipDir
		}
		if !d.IsDir() && parent == metaDirName && !trunks[strings.TrimSuffix(d.Name(), ".json")] {
			return nil
		}
		return copyEntry(path, filepath.Join(dst, rel), d)
	})
}

func copyEntry(from, to string, d fs.DirEntry) error {
	fi, err := d.Info()
	if err != nil {
		return err
	}
	switch {
	case d.IsDir():
		if err := os.MkdirAll(to, fi.Mode().Perm()); err != nil {
			return err
		}
		return nil
	case fi.Mode().IsRegular():
		if err := copyFile(from, to, fi); err != nil {
			return err
		}
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(from)
		if err != nil {
			return err
		}
		return os.Symlink(target, to)
	default:
		return nil
	}
	return os.Chtimes(to, fi.ModTime(), fi.ModTime())
}

func copyFile(from, to string, fi fs.FileInfo) error {
	in, err := os.Open(from)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if err := cloneOrCopy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func cloneOrCopy(out, in *os.File) error {
	if cloneFile(out, in) == nil {
		return nil
	}
	_, err := io.Copy(out, in)
	return err
}
