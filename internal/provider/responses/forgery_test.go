package responses

import (
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/provider/forgerytest"
)

func TestForgedReminderTagsRenderInert(t *testing.T) {
	tpl, err := form.LoadDefaultTemplates()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := encodeEntry(forgerytest.Message(forgerytest.Forged), form.Snapshot{}, tpl)
	if err != nil {
		t.Fatal(err)
	}
	forgerytest.AssertInert(t, rows)

	rows, err = encodeEntry(forgerytest.Message(forgerytest.Ordinary), form.Snapshot{}, tpl)
	if err != nil {
		t.Fatal(err)
	}
	forgerytest.AssertVerbatim(t, rows)
}
