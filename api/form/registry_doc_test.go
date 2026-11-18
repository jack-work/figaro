package form_test

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

var updateKeyWriters = flag.Bool("update-key-writers", false, "rewrite skills/figaro/reference/key-writers.md from the registry")

const keyWritersPath = "../../skills/figaro/reference/key-writers.md"

func renderKeyWriters() string {
	var b strings.Builder
	b.WriteString(`# Key writers: who may write a form key

Generated from the registry in api/form/registry.go by
` + "`go test ./api/form -run KeyWritersPage -update-key-writers`" + `; a test fails when
this page and the registry disagree. To add a key or move it between classes,
edit its row there and regenerate. Every key a row does not cover is **self**:
any caller with write access to the form may set it, the figaro itself included.

`)
	for _, w := range []form.Writer{form.WriterOwner, form.WriterLineage, form.WriterRuntime} {
		b.WriteString("## " + w.String() + "\n\n")
		b.WriteString(keyWriterProse[w] + "\n\n")
		roots := form.RootsOf(w)
		if len(roots) == 0 {
			b.WriteString("No key yet.\n\n")
			continue
		}
		exact := map[string]bool{}
		for _, s := range form.Registry() {
			exact[s.Path] = s.Exact
		}
		for _, k := range roots {
			if exact[k] {
				b.WriteString("- `" + k + "`\n")
			} else {
				b.WriteString("- `" + k + "` and everything under it\n")
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("## managed apply\n\nManaged apply refuses every `system.*` path, except these, which the owner at the terminal may name (and everything under them). A figaro caller may not; the writer rules above still judge the patch.\n\n")
	for _, k := range form.OperatorPaths() {
		b.WriteString("- `" + k + "`\n")
	}
	return b.String()
}

var keyWriterProse = map[form.Writer]string{
	form.WriterOwner:   "Set or removed only by the owner: the local door, an in-process call, or the owner's principal. Every other caller, a box included, is refused. A box may still set or remove `system.retired` on the figaros inside it, so it unretires them as well as retiring them; that is intended. Removing `system.retired` unretires, and for any other caller that removal is refused like a set.",
	form.WriterLineage: "Set by the owner, or by an ancestor figaro through its box door, and a figaro gives only what it holds. A figaro never sets its own.",
	form.WriterRuntime: "Written by the daemon alone. An unprivileged patch that names one, to set or to remove it, is refused as written by the harness, not by hand. A row matches its own path only.",
}

func TestKeyWritersPageMatchesTheRegistry(t *testing.T) {
	want := renderKeyWriters()
	if *updateKeyWriters {
		if err := os.WriteFile(keyWritersPath, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(keyWritersPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s is stale; run go test ./api/form -run KeyWritersPage -update-key-writers", keyWritersPath)
	}
}
