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

// A COMMENT THAT NAMES A TEST IS A CITATION, AND A CITATION THAT DOES NOT
// RESOLVE IS A LIE THE SUITE CANNOT SEE.
//
// TestSkillPathsCitedFromOutsideResolve does this for file paths. This does it
// for the other thing comments cite: the name of the test that is supposed to
// be holding the claim up. "Asserted by TestX" is the strongest sentence a
// comment can contain -- it tells a reader the claim IS under test -- and it is
// worth exactly nothing if TestX does not exist.
//
// It was written because that happened: a comment in
// internal/store/translate_on_append.go named a write-path assistant test that
// never existed under that name, within an hour of the citation being written,
// by the author of the test it meant to name.
//
// A CITATION RESOLVES IF IT IS A PREFIX OF A REAL NAME, because naming a
// FAMILY is legitimate -- "see TestSmoke" for TestSmoke_DetachedTail, or a
// benchmark group one would pass to -run. Only a name that prefixes nothing at
// all is dead.
//
// AND A TOMBSTONE IS NOT A DEAD CITATION. A comment saying a test IS DELETED,
// is GONE, or was RENAMED is naming something that must not exist, and it is
// the one honest way to record why a guard is absent.
func TestTestNamesCitedInCommentsExist(t *testing.T) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	repo := strings.TrimSpace(string(out))

	var (
		// A citation is a Test-prefixed identifier appearing in a // comment.
		cited = regexp.MustCompile(`\b(Test|Benchmark|Fuzz|Example)[A-Z][A-Za-z0-9_]{3,}\b`)
		// A tombstone names a test in order to say it is not there.
		tombstone = regexp.MustCompile(`(?i)\b(deleted|gone|removed|no longer|renamed|replaces|replaced by|superseded)\b`)
		// A definition is a top-level func with that name.
		defined = regexp.MustCompile(`(?m)^func\s+((?:Test|Benchmark|Fuzz|Example)[A-Za-z0-9_]+)\s*\(`)
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
