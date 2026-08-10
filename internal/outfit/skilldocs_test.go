package outfit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The skill tree ships inside the binary (flake.nix copies $src/skills to
// $out/share/figaro/skills), so a reader on a machine with no checkout follows
// its links with nothing to fall back on. A broken one is not a typo there, it
// is a dead end.
//
// This is the loader's package because the loader is what puts these files in
// front of an agent; if the tree is malformed, this is where it is noticed.

// skillsDir is the tree under test, resolved from this package's location so
// the test does not care where it is run from.
func skillsDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("no skills tree at %s: %v", dir, err)
	}
	return dir
}

// mdLink matches an inline markdown link target, minus any #fragment.
var mdLink = regexp.MustCompile(`\]\(([^)\s]+?)(#[^)]*)?\)`)

func eachSkillLink(t *testing.T, fn func(src, target string)) {
	t.Helper()
	root := skillsDir(t)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range mdLink.FindAllStringSubmatch(string(body), -1) {
			target := m[1]
			switch {
			case strings.HasPrefix(target, "http://"),
				strings.HasPrefix(target, "https://"),
				strings.HasPrefix(target, "mailto:"):
				continue
			}
			fn(path, target)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestSkillLinksResolve: every relative link in the tree points at something
// that exists. A restructure that moves a file is the thing this catches.
func TestSkillLinksResolve(t *testing.T) {
	n := 0
	eachSkillLink(t, func(src, target string) {
		n++
		if _, err := os.Stat(filepath.Join(filepath.Dir(src), target)); err != nil {
			t.Errorf("%s: link to %q does not resolve", rel(t, src), target)
		}
	})
	if n == 0 {
		t.Fatal("no links found; the walk is broken, not the tree")
	}
	t.Logf("checked %d links", n)
}

// TestSkillLinksStayInTheTree: a link must not escape skills/.
//
// The tree ships in the binary WITHOUT the repository around it, so a link out
// to a sibling directory resolves for the author and is a dead end for every
// reader who installed figaro rather than cloned it. escapes lists the ones
// that exist on purpose; each needs a reason, and "it works on my checkout" is
// not one.
func TestSkillLinksStayInTheTree(t *testing.T) {
	escapes := map[string]string{
		// cli.md cites the graft design, which is a repo proposal and has
		// deliberately not been folded into the shipped tree.
		"../../proposals/aria-graft.md": "repo-only proposal, cited as provenance",
	}
	root := skillsDir(t)
	eachSkillLink(t, func(src, target string) {
		abs, err := filepath.Abs(filepath.Join(filepath.Dir(src), target))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return
		}
		if _, ok := escapes[target]; ok {
			return
		}
		t.Errorf("%s: link to %q leaves the skill tree; it will be a dead end "+
			"for a reader who installed figaro rather than cloned it. Fold the "+
			"content in, or add it to escapes with a reason.", rel(t, src), target)
	})
}

// TestSkillLinkLabelsMatchTargets: a label that names a FILE must name the file
// it links to. Four labels once read turn-addressing.md, a name that had not
// existed for months, because a rename fixed the targets and left the text.
func TestSkillLinkLabelsMatchTargets(t *testing.T) {
	labelled := regexp.MustCompile(`\[([^\]]+\.md)\]\(([^)\s]+?)(#[^)]*)?\)`)
	root := skillsDir(t)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range labelled.FindAllStringSubmatch(string(body), -1) {
			label, target := m[1], m[2]
			if label == target || strings.HasSuffix(target, "/"+label) {
				continue
			}
			t.Errorf("%s: link labelled %q points at %q", rel(t, path), label, target)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestSkillPathsCitedFromOutsideResolve: a path into the doc tree written in a
// Go comment or a README ELSEWHERE in the repo must still name a file.
//
// The three tests above walk the tree and cannot see a citation from outside
// it, which is where the last two restructures left twenty-three dead ones:
// internal/tape/tape.go and internal/cli/testdata/tapes/README.md still said
// contributing/tapes.md after tapes.md moved to debugging/, and twenty-one
// comments still said docs/<x>.md years after docs/ stopped holding any of
// them — including four naming turn-addressing.md, the same vanished file the
// label test caught inside the tree. A reader following any of them landed
// nowhere, and the whole suite was green.
//
// docs/ is matched as well as skills/ because that is where these files USED
// to live: a citation that still says docs/ is the exact shape of the mistake.
func TestSkillPathsCitedFromOutsideResolve(t *testing.T) {
	root := skillsDir(t)
	repo := filepath.Dir(root)
	cite := regexp.MustCompile(`\b(?:skills/figaro|docs)/[A-Za-z0-9_./-]+\.md`)
	n := 0
	err := filepath.WalkDir(repo, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "skills", "result": // the tree itself is walked above
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".md":
		default:
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range cite.FindAllString(string(body), -1) {
			n++
			if _, err := os.Stat(filepath.Join(repo, m)); err != nil {
				r, _ := filepath.Rel(repo, path)
				t.Errorf("%s: cites %q, which does not exist", r, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("checked %d citations from outside the tree", n)
}

func rel(t *testing.T, p string) string {
	t.Helper()
	if r, err := filepath.Rel(skillsDir(t), p); err == nil {
		return r
	}
	return p
}
