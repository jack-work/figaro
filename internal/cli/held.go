package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

const dyingGrace = 3 * time.Second

type heldSend struct {
	aria string
	text string
}

type heldSends struct {
	mu   sync.Mutex
	away bool
	list []heldSend
	lost []heldSend
}

var heldLedger struct {
	sync.Mutex
	sessions []*heldSends
}

func trackHeld(h *heldSends) {
	heldLedger.Lock()
	heldLedger.sessions = append(heldLedger.sessions, h)
	heldLedger.Unlock()
}

func reportUnsentHeld(w io.Writer) {
	heldLedger.Lock()
	sessions := heldLedger.sessions
	heldLedger.sessions = nil
	heldLedger.Unlock()
	for _, h := range sessions {
		h.mu.Lock()
		unsent := append(h.lost, h.list...)
		h.list, h.lost = nil, nil
		h.mu.Unlock()
		for _, s := range unsent {
			fmt.Fprintf(w, "not sent to %s: %s\n", ariaName(s.aria), s.text)
		}
	}
}

func daemonGoingDown(err error, done <-chan struct{}) bool {
	var jerr *jkrpc.Error
	if errors.As(err, &jerr) {
		return strings.Contains(jerr.Message, rpc.DaemonStopping)
	}
	select {
	case <-done:
		return true
	case <-time.After(dyingGrace):
		return false
	}
}

func (in *interactiveInput) mayHold() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.ownsSubject && in.loaded != nil
}

func (in *interactiveInput) holdWhileAway(text string) bool {
	aria := in.currentID()
	in.held.mu.Lock()
	defer in.held.mu.Unlock()
	if !in.held.away {
		return false
	}
	in.held.list = append(in.held.list, heldSend{aria: aria, text: text})
	return true
}

func (in *interactiveInput) hold(text string) (away bool) {
	aria := in.currentID()
	in.held.mu.Lock()
	defer in.held.mu.Unlock()
	in.held.list = append(in.held.list, heldSend{aria: aria, text: text})
	return in.held.away
}

func (in *interactiveInput) heldNote() string {
	in.held.mu.Lock()
	n := len(in.held.list)
	in.held.mu.Unlock()
	switch n {
	case 0:
		return "sent"
	case 1:
		return "the angelus is away: 1 send held until it is back"
	}
	return fmt.Sprintf("the angelus is away: %d sends held until it is back", n)
}

func (in *interactiveInput) setAway() {
	in.held.mu.Lock()
	in.held.away = true
	in.held.mu.Unlock()
}

func (in *interactiveInput) sendHeld(ctx context.Context) {
	sent := 0
	for {
		in.held.mu.Lock()
		if len(in.held.list) == 0 {
			in.held.away = false
			in.held.mu.Unlock()
			break
		}
		h := in.held.list[0]
		in.held.mu.Unlock()
		if err := in.sendHeldOne(ctx, h, in.currentID()); err != nil {
			in.held.mu.Lock()
			in.held.away = false
			n := len(in.held.list)
			in.held.mu.Unlock()
			in.noteErr(fmt.Sprintf("a held send failed: %s (%d still held)", wireErrorText(err), n))
			return
		}
		in.held.mu.Lock()
		in.held.list = in.held.list[1:]
		in.held.mu.Unlock()
		sent++
	}
	if sent > 0 {
		in.note(fmt.Sprintf("%d held send(s) delivered", sent))
	}
}

func (in *interactiveInput) sendHeldOne(ctx context.Context, h heldSend, current string) error {
	if h.aria == current {
		_, _, err := sendVerb(ctx, verbEnv{}, sendPlan{prompt: h.text}, in.aria(), current)
		return err
	}
	env, err := in.verbEnv()
	if err != nil {
		return err
	}
	_, _, err = sendVerb(ctx, env, sendPlan{spec: h.aria, prompt: h.text}, nil, "")
	return err
}

func (in *interactiveInput) dropHeld(id string) {
	in.held.mu.Lock()
	n := len(in.held.list)
	in.held.lost = append(in.held.lost, in.held.list...)
	in.held.list = nil
	in.held.away = false
	in.held.mu.Unlock()
	if n > 0 {
		in.noteErr(fmt.Sprintf("%d held send(s) not delivered: %s did not come back; they are printed when you leave", n, ariaName(id)))
	}
}
