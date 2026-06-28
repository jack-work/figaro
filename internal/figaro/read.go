package figaro

import "github.com/jack-work/figaro/internal/livelog/aria"

// Read pulls one aria read caught up from sinceLT — the catch-up half of the
// same paginated read the live MethodAriaFrame stream pushes. A (re)connecting
// client reads from its last LT, then follows the live frames; application is
// idempotent, so a catch-up/live overlap can't double-apply.
func (a *Agent) Read(sinceLT int) aria.AriaRead {
	return a.ariaSrv.Read(sinceLT)
}
