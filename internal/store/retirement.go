package store

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
)

const SystemRetirementKey = "system.retirement"

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

func RetiredIn(snap form.Snapshot) bool {
	raw, ok := snap.Get(SystemRetiredKey)
	return ok && RetiredReason(lifetimeString(raw)) != ""
}

type Retirement struct {
	Tokens  int
	Percent float64
	TTL     time.Duration
}

func (r Retirement) Set() bool { return r.Tokens > 0 || r.Percent > 0 }

func (r Retirement) Threshold(window int) int {
	if r.Tokens > 0 {
		return r.Tokens
	}
	if r.Percent > 0 && window > 0 {
		return int(float64(window) * r.Percent / 100)
	}
	return 0
}

func RetirementOf(snap form.Snapshot) (Retirement, error) {
	var r Retirement
	raw, ok := snap.Get(SystemRetirementKey + ".context_threshold")
	if !ok {
		return r, nil
	}
	if err := r.parseThreshold(raw); err != nil {
		return Retirement{}, err
	}
	if raw, ok := snap.Get(SystemRetirementKey + ".ttl"); ok {
		ttl, err := ParseTTL(lifetimeString(raw))
		if err != nil {
			return Retirement{}, fmt.Errorf("%s.ttl: %w", SystemRetirementKey, err)
		}
		r.TTL = ttl
	}
	return r, nil
}

func (r *Retirement) parseThreshold(raw json.RawMessage) error {
	s := strings.TrimSpace(lifetimeString(raw))
	bad := fmt.Errorf("%s.context_threshold %s: want input tokens or a percentage of the context window (\"60%%\")", SystemRetirementKey, raw)
	if pct, ok := strings.CutSuffix(s, "%"); ok {
		p, err := strconv.ParseFloat(strings.TrimSpace(pct), 64)
		if err != nil || p <= 0 || p > 100 {
			return bad
		}
		r.Percent = p
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return bad
	}
	r.Tokens = n
	return nil
}

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
