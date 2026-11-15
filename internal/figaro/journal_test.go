package figaro

// Durability precedes visibility, and visibility is not optional.

import (
	"errors"
	"testing"

	"github.com/jack-work/figaro/internal/arialog"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

func TestAppendingAlwaysAnnounces(t *testing.T) {
	published := 0
	j := &journal{
		log:     store.GuardIR(arialog.NewMemLog[message.Message]()),
		publish: func() { published++ },
	}
	for i := 0; i < 3; i++ {
		if _, err := j.Append(arialog.Entry[message.Message]{
			Payload: message.Message{Role: message.RoleInput},
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if published != 3 {
		t.Fatalf("three durable records produced %d announcements. A record that is "+
			"durable and unannounced is invisible for as long as it takes something "+
			"ELSE to trigger a frame -- which is a provider's time-to-first-token, "+
			"and reads to a user as latency", published)
	}
}

// A failed write announces nothing.
func TestAFailedAppendAnnouncesNothing(t *testing.T) {
	published := 0
	j := &journal{
		log:     failingLog{},
		publish: func() { published++ },
	}
	if _, err := j.Append(arialog.Entry[message.Message]{
		Payload: message.Message{Role: message.RoleInput},
	}); err == nil {
		t.Fatal("a failing log reported success")
	}
	if published != 0 {
		t.Fatalf("a FAILED append announced %d times. The client would show a message "+
			"the log does not have, and a restart would make it vanish", published)
	}
}

type failingLog struct{ arialog.Log[message.Message] }

func (failingLog) Append(arialog.Entry[message.Message]) (arialog.Entry[message.Message], error) {
	return arialog.Entry[message.Message]{}, errors.New("disk is on fire")
}
