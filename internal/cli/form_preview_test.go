package cli

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
)

func oldFormValuePreview(raw json.RawMessage) string {
	return clipToWidthEllipsis(strings.Join(strings.Fields(string(raw)), " "), 120)
}

// The preview stops reading early and must still say what the old one said.
func TestFormValuePreviewMatchesFieldsJoin(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 7))
	alphabet := []string{"a", "b", " ", "  ", "\n", "\t", "日", "本", "é", "\u00a0", "x", "\u3000"}
	for i := range 3000 {
		var b strings.Builder
		for range r.IntN(400) {
			b.WriteString(alphabet[r.IntN(len(alphabet))])
		}
		raw := json.RawMessage(b.String())
		if got, want := formValuePreview(raw), oldFormValuePreview(raw); got != want {
			t.Fatalf("case %d %q:\n got %q\nwant %q", i, raw, got, want)
		}
	}
}
