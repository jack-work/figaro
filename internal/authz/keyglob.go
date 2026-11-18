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
	if a == "" {
		return true
	}
	if b == "" {
		return false
	}
	return a == b || KeyGlobMatch(a, b)
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
