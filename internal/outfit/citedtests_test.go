package outfit_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A Test-prefixed name in a comment is a citation and must prefix a real test name.
// A tombstone (a comment saying the test is deleted, gone or renamed) is not one.
func TestTestNamesCitedInCommentsExist(t *testing.T) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	repo := strings.TrimSpace(string(out))

	var (
		cited     = regexp.MustCompile(`\b(Test|Benchmark|Fuzz|Example)[A-Z][A-Za-z0-9_]{3,}\b`)
		tombstone = regexp.MustCompile(`(?i)\b(deleted|gone|removed|no longer|renamed|replaces|replaced by|superseded)\b`)
		defined   = regexp.MustCompile(`(?m)^func\s+((?:Test|Benchmark|Fuzz|Example)[A-Za-z0-9_]+)\s*\(`)
	)

	have := map[string]bool{}
	type site struct{ file, name string }
	var claims []site

	err = filepath.WalkDir(repo, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "result", "vendor", "scratch":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, m := range defined.FindAllStringSubmatch(string(body), -1) {
			have[m[1]] = true
		}
		rel, _ := filepath.Rel(repo, path)
		for _, line := range strings.Split(string(body), "\n") {
			i := strings.Index(line, "//")
			if i < 0 {
				continue
			}
			if tombstone.MatchString(line[i:]) {
				continue
			}
			for _, name := range cited.FindAllString(line[i:], -1) {
				claims = append(claims, site{rel, name})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(claims) == 0 {
		t.Fatal("no cited test names found anywhere: this check cannot fail, which makes it worthless")
	}

	names := make([]string, 0, len(have))
	for n := range have {
		names = append(names, n)
	}
	resolves := func(cite string) bool {
		if have[cite] {
			return true
		}
		for _, n := range names {
			if strings.HasPrefix(n, cite) {
				return true
			}
		}
		return false
	}

	var dead []string
	for _, c := range claims {
		if !resolves(c.name) {
			dead = append(dead, c.file+": "+c.name)
		}
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		t.Fatalf("%d comment(s) cite a test that does not exist:\n\t%s",
			len(dead), strings.Join(dead, "\n\t"))
	}
	t.Logf("%d test-name citations across the tree, all resolving", len(claims))
}
