package figaro

import "github.com/jack-work/figaro/api/rpc"

func (a *Agent) SubmitDelivery(principal, delivery, text string) error {
	evt := event{
		typ:      eventUserPrompt,
		text:     text,
		segments: []promptSegment{{principal: principal, text: text, delivery: delivery}},
	}
	if !a.inbox.Send(evt) {
		return ErrStopping
	}
	if !a.turnActive() {
		a.publishRuntime(rpc.RuntimeAccepted, "")
	}
	return nil
}

func (a *Agent) UnansweredDeliveries() []string {
	var out []string
	for _, e := range a.inbox.Unanswered() {
		for _, seg := range e.segments {
			if seg.delivery != "" {
				out = append(out, seg.delivery)
			}
		}
	}
	return out
}
