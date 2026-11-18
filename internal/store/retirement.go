package store

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
)

const SystemRetirementKey = form.RetirementKey

const (
	SystemSuccessionWarnedKey = form.SuccessionWarnedKey
	SystemSuccessionFailedKey = "system.succession_failed"
)

const (
	RetiredManual  = "manual"
	RetiredContext = "context"
)

const (
	RetirementRetire         = form.RetirementRetire
	RetirementSucceed        = form.RetirementSucceed
	DefaultSuccessionTimeout = form.DefaultSuccessionTimeout
	DefaultRampTurns         = form.DefaultRampTurns
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

func RetiredIn(snap form.Snapshot) bool {
	raw, ok := form.RuntimeGet(snap, RetiredKey)
	return ok && RetiredReason(lifetimeString(raw)) != ""
}

type Retirement = form.Retirement

func RetirementOf(snap form.Snapshot) (Retirement, error) {
	raw, ok := snap.Get(SystemRetirementKey)
	if !ok || string(raw) == "null" {
		return Retirement{}, nil
	}
	return form.ParseRetirement(raw)
}

func ParseRetirement(raw json.RawMessage) (Retirement, error) { return form.ParseRetirement(raw) }

func TTLFromRetirement(createdAtMS int64, d time.Duration, now time.Time) string {
	var age time.Duration
	if createdAtMS > 0 && now.UnixMilli() > createdAtMS {
		age = now.Sub(time.UnixMilli(createdAtMS))
	}
	return (age + d).Round(time.Second).String()
}

func RetirePatch(reason string, createdAtMS int64, ttl time.Duration, now time.Time) form.Patch {
	keys := map[string]json.RawMessage{}
	keys[SystemRetiredKey], _ = json.Marshal(reason)
	if ttl > 0 {
		keys[SystemTTLKey], _ = json.Marshal(TTLFromRetirement(createdAtMS, ttl, now))
	}
	return form.Build(form.Snapshot{}, keys, nil)
}
