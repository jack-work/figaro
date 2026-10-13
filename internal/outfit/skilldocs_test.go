package outfit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The skill tree ships in the binary without the repository around it, so links must resolve inside it.

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

// escapes lists the links out of skills/ that exist on purpose.
func TestSkillLinksStayInTheTree(t *testing.T) {
	escapes := map[string]string{
		// The graft design is a repo proposal, deliberately not in the shipped tree.
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

func TestSkillLinkLabelsMatchTargets(t *testing.T) {
	// No \n in the label class, or an unmatched '[' above pairs with the next link.
	labelled := regexp.MustCompile(`\[([^\]\n]+\.md)\]\(([^)\s]+?)(#[^)]*)?\)`)
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

// A skills/ path cited from outside the tree must name a file; docs/ is matched because stale citations still say it.
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
