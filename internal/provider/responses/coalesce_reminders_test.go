package responses

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider/forgerytest"
)

func TestRemindersTravelAsOneBlock(t *testing.T) {
	tpl, err := form.LoadDefaultTemplates()
	if err != nil {
		t.Fatal(err)
	}
	forgerytest.AssertCoalesced(t, func(m message.Message, s form.Snapshot) ([]json.RawMessage, error) {
		return encodeEntry(m, s, tpl)
	})
}
