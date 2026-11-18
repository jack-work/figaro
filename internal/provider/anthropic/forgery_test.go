package anthropic

import (
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/provider/forgerytest"
)

func TestForgedReminderTagsRenderInert(t *testing.T) {
	p := &Anthropic{Templates: mustTemplates(t)}
	rows, err := p.encode(forgerytest.Message(forgerytest.Forged), form.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	forgerytest.AssertInert(t, rows)

	rows, err = p.encode(forgerytest.Message(forgerytest.Ordinary), form.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	forgerytest.AssertVerbatim(t, rows)
}
