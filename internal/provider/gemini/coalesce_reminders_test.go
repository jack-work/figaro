package gemini

import (
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/provider/forgerytest"
)

func TestRemindersTravelAsOneBlock(t *testing.T) {
	tpl, err := form.LoadDefaultTemplates()
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{Templates: tpl}
	forgerytest.AssertCoalesced(t, p.EncodeMessage)
}
