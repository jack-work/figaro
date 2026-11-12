package angelus

import (
	"context"
	"log/slog"
	"time"

	"github.com/jack-work/jkauthz"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/figaro"
)

type failureNoticeReceiver interface {
	SubmitFailureNotice(from, text string) error
}

const failureNoticeTimeout = 30 * time.Second

func (h *handlers) tellFailure(n figaro.FailureNotice) {
	h.angelus.Go("turn failure notice", func() {
		ctx, cancel := context.WithTimeout(context.Background(), failureNoticeTimeout)
		defer cancel()
		if err := h.senderMaySend(ctx, n); err != nil {
			slog.Warn("turn failure notice refused", "from", n.From, "to", n.To, "principal", n.Principal, "err", err)
			return
		}
		f, err := h.restoreByID(ctx, n.To, "failure.notice")
		if err != nil {
			slog.Warn("turn failure notice not delivered", "from", n.From, "to", n.To, "err", err)
			return
		}
		r, ok := f.(failureNoticeReceiver)
		if !ok {
			return
		}
		if err := r.SubmitFailureNotice(n.From, n.Text); err != nil {
			slog.Warn("turn failure notice refused by its receiver", "from", n.From, "to", n.To, "err", err)
		}
	})
}

func (h *handlers) senderMaySend(ctx context.Context, n figaro.FailureNotice) error {
	p, err := jkauthz.ParsePrincipal(n.Principal)
	if err != nil {
		return err
	}
	return h.allowed(authz.WithSubject(ctx, jkauthz.Subject{Principal: p}), rpc.VerbSend, authz.Aria(n.To))
}
