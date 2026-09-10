package figaro

// THE JOURNAL'S CONTRACT: durability precedes visibility, and visibility is
// not optional.
//
// The bug this guards against is not a crash. It is a record that becomes
// durable and stays INVISIBLE -- which looks like latency, reads like a slow
// wire, and cannot be fixed by making the wire faster. A queued message became
// part of the conversation, its queue row cleared, and the text appeared only
// when the next provider round produced its first token.

import (
	"errors"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

func TestAppendingAlwaysAnnounces(t *testing.T) {
	published := 0
	j := &journal{
		log:     store.GuardIR(store.NewMemLog[message.Message]()),
		publish: func() { published++ },
	}
	for i := 0; i < 3; i++ {
		if _, err := j.Append(store.Entry[message.Message]{
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

// A FAILED WRITE ANNOUNCES NOTHING. Nothing became true, so there is nothing
// to say; announcing here would put a message on screen that no restart would
// find.
func TestAFailedAppendAnnouncesNothing(t *testing.T) {
	published := 0
	j := &journal{
		log:     failingLog{},
		publish: func() { published++ },
	}
	if _, err := j.Append(store.Entry[message.Message]{
		Payload: message.Message{Role: message.RoleInput},
	}); err == nil {
		t.Fatal("a failing log reported success")
	}
	if published != 0 {
		t.Fatalf("a FAILED append announced %d times. The client would show a message "+
			"the log does not have, and a restart would make it vanish", published)
	}
}

type failingLog struct{ store.Log[message.Message] }

func (failingLog) Append(store.Entry[message.Message]) (store.Entry[message.Message], error) {
	return store.Entry[message.Message]{}, errors.New("disk is on fire")
}
