package angelus

import (
	"context"
	"log/slog"
)

const wokeByRestart = "restart"

func (hs *Handlers) Redeliver(ctx context.Context) {
	h := hs.h
	ids, err := h.angelus.Spool.Arias()
	if err != nil {
		slog.Error("redeliver: list the spool", "err", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if _, ok := h.angelus.Backend.Node(id); !ok {
			slog.Warn("redeliver: the aria is gone; dropping its spool", "id", id)
			if err := h.angelus.Spool.Remove(id); err != nil {
				slog.Warn("spool remove", "id", id, "err", err)
			}
			continue
		}
		if _, err := h.restoreByID(ctx, id, wokeByRestart); err != nil {
			slog.Error("redeliver: wake", "id", id, "err", err)
		}
	}
}
