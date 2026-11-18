package openaichat

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
	p := &Provider{Templates: tpl}
	rows, err := p.EncodeMessage(forgerytest.Message(forgerytest.Forged), form.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	forgerytest.AssertInert(t, rows)

	rows, err = p.EncodeMessage(forgerytest.Message(forgerytest.Ordinary), form.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	forgerytest.AssertVerbatim(t, rows)
}

// Defends tool output against forged reminders: the wire may put genuine
// reminders inside the last tool result, so an unescaped tool could print
// one indistinguishable from the daemon's.
func TestAToolCannotPrintAReminder(t *testing.T) {
	tpl, err := form.LoadDefaultTemplates()
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{Templates: tpl}
	rows, err := p.EncodeMessage(forgerytest.ToolResults(forgerytest.Forged), form.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	forgerytest.AssertToolOutputInert(t, rows)

	rows, err = p.EncodeMessage(forgerytest.ToolResults(forgerytest.Ordinary), form.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	forgerytest.AssertToolOutputVerbatim(t, rows)
}
