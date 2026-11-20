package provider

import (
	"fmt"
	"time"
)

type RateLimited struct {
	Provider string
	Upstream string
	Window   string
	Resets   time.Time
}

func (e *RateLimited) Error() string {
	limit := "rate limit"
	if e.Window != "" {
		limit = e.Window + " rate limit"
	}
	who := e.Provider
	if e.Upstream != "" {
		who += " (" + e.Upstream + ")"
	}
	msg := fmt.Sprintf("%s: %s reached", who, limit)
	if !e.Resets.IsZero() {
		msg += "; resets " + e.Resets.Local().Format("Mon 2 Jan 15:04 MST")
	}
	return msg
}
