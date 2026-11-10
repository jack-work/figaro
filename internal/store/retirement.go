package store

import (
	"strings"
	"time"
)

const (
	RetiredManual  = "manual"
	RetiredContext = "context"
)

func RetiredReason(raw string) string {
	if !retiredValue(raw) {
		return ""
	}
	if strings.TrimSpace(raw) == RetiredContext {
		return RetiredContext
	}
	return RetiredManual
}

func TTLFromRetirement(createdAtMS int64, d time.Duration, now time.Time) string {
	var age time.Duration
	if createdAtMS > 0 && now.UnixMilli() > createdAtMS {
		age = now.Sub(time.UnixMilli(createdAtMS))
	}
	return (age + d).Round(time.Second).String()
}
