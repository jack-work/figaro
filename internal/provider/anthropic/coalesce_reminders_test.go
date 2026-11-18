package anthropic

import (
	"testing"

	"github.com/jack-work/figaro/internal/provider/forgerytest"
)

func TestRemindersTravelAsOneBlock(t *testing.T) {
	p := &Anthropic{Templates: mustTemplates(t)}
	forgerytest.AssertCoalesced(t, p.encode)
}
