package authz

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/form"
)

const keySep = "#"

func splitKeyed(object string) (sel, glob string) {
	sel, glob, _ = strings.Cut(object, keySep)
	return sel, glob
}

func keyed(sel, glob string) string {
	if glob == "" {
		return sel
	}
	return sel + keySep + glob
}

func KeyGlobMatch(pattern, key string) bool { return form.KeyGlobMatch(pattern, key) }

func globCovers(a, b string) bool {
	switch {
	case a == "" || a == b:
		return true
	case b == "":
		return false
	}
	if class, ok := strings.CutPrefix(a, form.KeyClassPrefix); ok {
		return !strings.Contains(b, "*") && !strings.HasPrefix(b, form.KeyClassPrefix) && form.WriterOf(b).String() == class
	}
	if strings.HasPrefix(b, form.KeyClassPrefix) {
		return a == "**"
	}
	return segmentsCover(strings.Split(a, "."), strings.Split(b, "."))
}

func segmentsCover(a, b []string) bool {
	if len(a) == 0 {
		return len(b) == 0
	}
	if a[0] == "**" {
		return segmentsCover(a[1:], b) || (len(b) > 0 && segmentsCover(a, b[1:]))
	}
	if len(b) == 0 || b[0] == "**" {
		return false
	}
	if strings.Contains(b[0], "*") {
		if a[0] != "*" && a[0] != b[0] {
			return false
		}
	} else if !KeyGlobMatch(a[0], b[0]) {
		return false
	}
	return segmentsCover(a[1:], b[1:])
}

func validateKeyGlob(glob string) error {
	if glob == "" {
		return fmt.Errorf("a key glob after %s names nothing", keySep)
	}
	if class, ok := strings.CutPrefix(glob, form.KeyClassPrefix); ok {
		if class != form.WriterSelf.String() && class != form.WriterLineage.String() {
			return fmt.Errorf("key class %q: a grant names @self or @lineage; owner and runtime keys are granted to no figaro", glob)
		}
		return nil
	}
	for _, seg := range strings.Split(glob, ".") {
		if seg == "" {
			return fmt.Errorf("key glob %q has an empty segment", glob)
		}
		if strings.Contains(seg, "**") && seg != "**" {
			return fmt.Errorf("key glob %q: ** stands alone as a segment", glob)
		}
		if strings.ContainsAny(seg, keySep+" \t") {
			return fmt.Errorf("key glob %q contains a reserved character", glob)
		}
	}
	if literalPrefix(glob) == form.RuntimeRoot || strings.HasPrefix(literalPrefix(glob), form.RuntimeRoot+".") {
		return fmt.Errorf("key glob %q names runtime.*, which only the daemon writes and no grant reaches", glob)
	}
	return nil
}

func literalPrefix(glob string) string {
	segs := strings.Split(glob, ".")
	for i, s := range segs {
		if strings.Contains(s, "*") {
			return strings.Join(segs[:i], ".")
		}
	}
	return glob
}
