package render

import (
	"strings"
	"testing"
)

func benchMessage(paragraphs int) string {
	var b strings.Builder
	for i := range paragraphs {
		switch i % 4 {
		case 0:
			b.WriteString("Largo al factotum della città,\nlargo, largheeeetti, signori, di qua!\nLa ruta m'aspetta, non posso tardar,\nfrattanto fra voi qui devo girar.\n\n")
		case 1:
			b.WriteString("- a list item that runs long enough to wrap across the pane width once\n- another item\n  with a soft break inside it\n\n")
		case 2:
			b.WriteString("> a quoted line\n> and another that continues the quote\n\n")
		case 3:
			b.WriteString("```go\nfunc main() {\n\tprintln(\"ecco fatto\")\n}\n```\n\n")
		}
	}
	return b.String()
}

// The markdown render on the streaming path, a short and a long message,
// uncached: a growing message re-renders on every frame.
func BenchmarkProseMessage(b *testing.B) {
	for _, c := range []struct {
		name string
		md   string
	}{{"short", benchMessage(1)}, {"long", benchMessage(80)}} {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				_ = proseUncached(c.md, 80)
			}
		})
	}
}
