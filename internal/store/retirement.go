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
	SystemSuccessionWarnedKey = "system.succession_warned"
	SystemSuccessionFailedKey = "system.succession_failed"
)

const (
	RetiredManual  = "manual"
	RetiredContext = "context"
)

const (
	RetirementRetire  = "retire"
	RetirementSucceed = "succeed"

	DefaultSuccessionTimeout = 30 * time.Minute
	DefaultRampTurns         = 5
	successionWarnAt         = 0.8
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
	Tokens    int
	Percent   float64
	TTL       time.Duration
	Action    string
	Page      string
	Timeout   time.Duration
	Ramp      string
	RampTurns int
}

func (r Retirement) Succeeds() bool { return r.Action == RetirementSucceed }

func (r Retirement) WarnAt(window int) int {
	return int(float64(r.Threshold(window)) * successionWarnAt)
}

func (r Retirement) SuccessionTimeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return DefaultSuccessionTimeout
}

func (r Retirement) QuotedTurns() int {
	if r.RampTurns > 0 {
		return r.RampTurns
	}
	return DefaultRampTurns
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
	raw, ok := snap.Get(SystemRetirementKey)
	if !ok || string(raw) == "null" {
		return Retirement{}, nil
	}
	return ParseRetirement(raw)
}

func ParseRetirement(raw json.RawMessage) (Retirement, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return Retirement{}, fmt.Errorf("%s: want a table {context_threshold, ttl}, got %s", SystemRetirementKey, raw)
	}
	var r Retirement
	for k, v := range fields {
		switch k {
		case "context_threshold":
			if err := r.parseThreshold(v); err != nil {
				return Retirement{}, err
			}
		case "ttl":
			ttl, err := ParseTTL(lifetimeString(v))
			if err != nil || ttl < 0 || (ttl == 0 && lifetimeString(v) == "") {
				return Retirement{}, fmt.Errorf("%s.ttl %s: want a duration (90m, 6h, 2d)", SystemRetirementKey, v)
			}
			r.TTL = ttl
		case "action":
			switch a := lifetimeString(v); a {
			case RetirementRetire, RetirementSucceed:
				r.Action = a
			default:
				return Retirement{}, fmt.Errorf("%s.action %s: want retire or succeed", SystemRetirementKey, v)
			}
		case "page":
			if r.Page = strings.TrimSpace(lifetimeString(v)); r.Page == "" {
				return Retirement{}, fmt.Errorf("%s.page %s: want an aria or @role", SystemRetirementKey, v)
			}
		case "timeout":
			d, err := ParseTTL(lifetimeString(v))
			if err != nil || d <= 0 {
				return Retirement{}, fmt.Errorf("%s.timeout %s: want a positive duration (10m, 1h)", SystemRetirementKey, v)
			}
			r.Timeout = d
		case "ramp":
			if r.Ramp = strings.TrimSpace(lifetimeString(v)); r.Ramp == "" {
				return Retirement{}, fmt.Errorf("%s.ramp %s: want a file path", SystemRetirementKey, v)
			}
		case "ramp_turns":
			n, err := strconv.Atoi(strings.TrimSpace(lifetimeString(v)))
			if err != nil || n <= 0 {
				return Retirement{}, fmt.Errorf("%s.ramp_turns %s: want a count of turns, 1 or more", SystemRetirementKey, v)
			}
			r.RampTurns = n
		default:
			return Retirement{}, fmt.Errorf("%s.%s: unknown field; want context_threshold, ttl, action, page, timeout, ramp or ramp_turns", SystemRetirementKey, k)
		}
	}
	if r.Action == "" {
		r.Action = RetirementRetire
	}
	if !r.Set() {
		return Retirement{}, fmt.Errorf("%s.context_threshold: required", SystemRetirementKey)
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
