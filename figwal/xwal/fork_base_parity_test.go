package xwal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jack-work/figaro/figwal/disk"
)

// Every .fork reader moved onto forkBaseIn while keeping the answer its site
// gave before. The oracles below are the pre-accessor reads, verbatim from
// 06986aa4 (forkBaseOf from the commit that made it refuse); each site's new
// mapping must give the same value, the same error class and the same
// recovery side effects.

func oracleReadForkBaseFile(dir string) (uint64, error) {
	path := filepath.Join(dir, ".fork")
	if err := recoverAtomicReplacement(path); err != nil {
		return 0, err
	}
	if _, err := os.Stat(path); err != nil {
		return 0, err
	}
	base, err := disk.ForkBaseOf(dir)
	if err == nil && base == 0 {
		err = fmt.Errorf("xwal: malformed fork marker %q", path)
	}
	return base, err
}

func oracleReadForkBase(dir string) (uint64, error) {
	if _, err := os.Stat(filepath.Join(dir, ".fork")); err != nil {
		return 0, err
	}
	return disk.ForkBaseOf(dir)
}

func oracleForkBaseOf(key, dir string) (uint64, error) {
	path := filepath.Join(dir, ".fork")
	if _, err := os.Stat(path); err != nil {
		return 0, &ForkBaseError{Node: key, Path: path, Err: err}
	}
	base, err := disk.ForkBaseOf(dir)
	if err != nil {
		return 0, &ForkBaseError{Node: key, Path: path, Err: err}
	}
	return base, nil
}

func oracleRetainRead(dir string) (uint64, error) { return disk.ForkBaseOf(dir) }

func errClass(err error) string {
	var fe *ForkBaseError
	switch {
	case err == nil:
		return "nil"
	case errors.As(err, &fe):
		return "forkbase:" + errClass(fe.Err)
	case errors.Is(err, fs.ErrNotExist):
		return "notexist"
	case errors.Is(err, fs.ErrPermission):
		return "permission"
	}
	return "other:" + err.Error()
}

type siteAnswer struct {
	base  uint64
	class string
}

func (a siteAnswer) String() string { return fmt.Sprintf("(%d, %s)", a.base, a.class) }

func answer(base uint64, err error) siteAnswer { return siteAnswer{base, errClass(err)} }

func (a siteAnswer) under(root string) siteAnswer {
	a.class = strings.ReplaceAll(a.class, root, "<root>")
	return a
}

type forkSite struct {
	name   string
	oracle func(t *Trunks, key, channel string) siteAnswer
	now    func(t *Trunks, key, channel string) siteAnswer
}

var forkSites = []forkSite{
	{
		"strict (reparent, prune, repair, channel opener, flatten)",
		func(t *Trunks, key, ch string) siteAnswer {
			return answer(oracleReadForkBaseFile(t.channelDir(ch, key)))
		},
		func(t *Trunks, key, ch string) siteAnswer {
			base, present, err := t.recoveredChannelBase(key, ch)
			return answer(strictForkBase(t.channelDir(ch, key), base, present, err))
		},
	},
	{
		"strict by directory",
		func(t *Trunks, key, ch string) siteAnswer {
			return answer(oracleReadForkBaseFile(t.channelDir(ch, key)))
		},
		func(t *Trunks, key, ch string) siteAnswer {
			return answer(readForkBaseStrict(t.channelDir(ch, key)))
		},
	},
	{
		"ownerOf (readForkBase)",
		func(t *Trunks, key, ch string) siteAnswer {
			if ch != t.main {
				return siteAnswer{}
			}
			return answer(oracleReadForkBase(t.irDir(key)))
		},
		func(t *Trunks, key, ch string) siteAnswer {
			if ch != t.main {
				return siteAnswer{}
			}
			return answer(t.readForkBase(key))
		},
	},
	{
		"lineage (forkBaseOf)",
		func(t *Trunks, key, ch string) siteAnswer {
			if ch != t.main {
				return siteAnswer{}
			}
			return answer(oracleForkBaseOf(key, t.irDir(key)))
		},
		func(t *Trunks, key, ch string) siteAnswer {
			if ch != t.main {
				return siteAnswer{}
			}
			return answer(t.forkBaseOf(key))
		},
	},
	{
		"translations v5 repair (Layout.ForkBase)",
		func(t *Trunks, key, ch string) siteAnswer {
			if ch != t.main {
				return siteAnswer{}
			}
			return answer(oracleForkBaseOf(key, t.irDir(key)))
		},
		func(t *Trunks, key, ch string) siteAnswer {
			if ch != t.main {
				return siteAnswer{}
			}
			return answer(Layout{Main: t.main}.ForkBase(t.root, key))
		},
	},
	{
		"retention pin (pinsOn)",
		func(t *Trunks, key, ch string) siteAnswer {
			return answer(oracleRetainRead(t.channelDir(ch, key)))
		},
		func(t *Trunks, key, ch string) siteAnswer {
			base, _, err := t.channelBase(key, ch)
			return answer(base, err)
		},
	},
}

func dirState(t *testing.T, dir string) string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		out = append(out, e.Name()+"="+string(b))
	}
	sort.Strings(out)
	return strings.Join(out, ";")
}

func TestEveryForkReaderKeepsItsSiteMapping(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	const key = "k"
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"absent", func(*testing.T, string) {}},
		{"empty", writeFork("")},
		{"garbage", writeFork("garbage\n")},
		{"no base line", writeFork("other=1\n")},
		{"base=x", writeFork("base=x\n")},
		{"base=0", writeFork("base=0\n")},
		{"base=1", writeFork("base=1\n")},
		{"base=7", writeFork("base=7\n")},
		{"unreadable", func(t *testing.T, dir string) {
			writeFork("base=7\n")(t, dir)
			if err := os.Chmod(filepath.Join(dir, ".fork"), 0); err != nil {
				t.Fatal(err)
			}
		}},
		{"pending, final lost, tmp kept", func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, ".fork.replace-pending"), "replace\n")
			mustWrite(t, filepath.Join(dir, ".fork.tmp"), "base=9\n")
		}},
		{"pending, final lost, invalid kept", func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, ".fork.replace-pending"), "replace\n")
			mustWrite(t, filepath.Join(dir, ".fork.invalid"), "base=5\n")
		}},
		{"pending, final present", func(t *testing.T, dir string) {
			writeFork("base=4\n")(t, dir)
			mustWrite(t, filepath.Join(dir, ".fork.replace-pending"), "replace\n")
			mustWrite(t, filepath.Join(dir, ".fork.tmp"), "base=9\n")
		}},
		{"pending, nothing to recover", func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, ".fork.replace-pending"), "replace\n")
		}},
	}
	for _, ch := range []string{"ir", "translations"} {
		for _, c := range cases {
			for _, site := range forkSites {
				t.Run(ch+"/"+c.name+"/"+site.name, func(t *testing.T) {
					was, is := &Trunks{root: t.TempDir(), main: "ir"}, &Trunks{root: t.TempDir(), main: "ir"}
					for _, tr := range []*Trunks{was, is} {
						dir := tr.channelDir(ch, key)
						if err := os.MkdirAll(dir, 0o755); err != nil {
							t.Fatal(err)
						}
						c.setup(t, dir)
						t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, ".fork"), 0o644) })
					}
					want, got := site.oracle(was, key, ch).under(was.root), site.now(is, key, ch).under(is.root)
					if want != got {
						t.Fatalf("site answered %v, before the accessor %v", got, want)
					}
					_ = os.Chmod(filepath.Join(was.channelDir(ch, key), ".fork"), 0o644)
					_ = os.Chmod(filepath.Join(is.channelDir(ch, key), ".fork"), 0o644)
					if a, b := dirState(t, was.channelDir(ch, key)), dirState(t, is.channelDir(ch, key)); a != b {
						t.Fatalf("recovery differs:\nbefore the accessor %s\nnow                %s", a, b)
					}
				})
			}
		}
	}
}

func writeFork(body string) func(t *testing.T, dir string) {
	return func(t *testing.T, dir string) { mustWrite(t, filepath.Join(dir, ".fork"), body) }
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// FIGWAL_PARITY_ROOT names a copy of a real store's arias directory. Every
// node directory of every channel is asked through every site, old and new.
// The copy may be written: the recovering sites complete a pending
// replacement, so never point this at a live store.
func TestEveryForkReaderKeepsItsSiteMappingOnAStore(t *testing.T) {
	root := os.Getenv("FIGWAL_PARITY_ROOT")
	if root == "" {
		t.Skip("FIGWAL_PARITY_ROOT names a copy of a store's arias directory")
	}
	tr := &Trunks{root: root, main: "ir"}
	var channels []string
	tops, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range tops {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.Name() != "translations-v2" {
			channels = append(channels, e.Name())
			continue
		}
		subs, err := os.ReadDir(filepath.Join(root, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range subs {
			if s.IsDir() {
				channels = append(channels, e.Name()+"/"+s.Name())
			}
		}
	}
	asked, pending, present := 0, 0, 0
	for _, ch := range channels {
		ents, err := os.ReadDir(filepath.Join(root, ch))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			key := keyName(e.Name())
			dir := tr.channelDir(ch, key)
			if pathExists(filepath.Join(dir, ".fork.replace-pending")) {
				pending++
				continue
			}
			if pathExists(filepath.Join(dir, ".fork")) {
				present++
			}
			for _, site := range forkSites {
				if want, got := site.oracle(tr, key, ch), site.now(tr, key, ch); want != got {
					t.Errorf("%s %s, %s: now %v, before the accessor %v", ch, key, site.name, got, want)
				}
			}
			if _, _, err := tr.channelBase(key, ch); err != nil {
				t.Errorf("%s %s: the retention read fails, so pinsOn would stop retention: %v", ch, key, err)
			}
			asked++
		}
	}
	t.Logf("channels %d, node directories %d (%d with a .fork), skipped with a pending replacement %d, sites %d", len(channels), asked, present, pending, len(forkSites))
	if asked == 0 {
		t.Fatal("no node directory under FIGWAL_PARITY_ROOT")
	}
}
