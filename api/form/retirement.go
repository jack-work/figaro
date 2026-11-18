package form

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const RetirementKey = "system.retirement"

const (
	RetirementRetire  = "retire"
	RetirementSucceed = "succeed"

	DefaultSuccessionTimeout = 30 * time.Minute
	DefaultRampTurns         = 5
	successionWarnAt         = 0.8
)

type Retirement struct {
	FallbackTokens int
	Tokens         int
	Percent        float64
	TTL            time.Duration
	Action         string
	Page           string
	Timeout        time.Duration
	Ramp           string
	RampTurns      int
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
	if window <= 0 {
		return r.FallbackTokens
	}
	return 0
}

func ParseRetirement(raw json.RawMessage) (Retirement, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return Retirement{}, fmt.Errorf("%s: want a table {context_threshold, ttl}, got %s", RetirementKey, raw)
	}
	var r Retirement
	for k, v := range fields {
		switch k {
		case "context_threshold":
			if err := r.parseThreshold(v); err != nil {
				return Retirement{}, err
			}
		case "fallback_tokens":
			if err := json.Unmarshal(v, &r.FallbackTokens); err != nil || r.FallbackTokens <= 0 {
				return Retirement{}, fmt.Errorf("%s.fallback_tokens: want a positive token count", RetirementKey)
			}
		case "ttl":
			ttl, err := ParseLifetime(ScalarString(v))
			if err != nil || ttl < 0 || (ttl == 0 && ScalarString(v) == "") {
				return Retirement{}, fmt.Errorf("%s.ttl %s: want a duration (90m, 6h, 2d)", RetirementKey, v)
			}
			r.TTL = ttl
		case "action":
			switch a := ScalarString(v); a {
			case RetirementRetire, RetirementSucceed:
				r.Action = a
			default:
				return Retirement{}, fmt.Errorf("%s.action %s: want retire or succeed", RetirementKey, v)
			}
		case "page":
			if r.Page = strings.TrimSpace(ScalarString(v)); r.Page == "" {
				return Retirement{}, fmt.Errorf("%s.page %s: want an aria or @role", RetirementKey, v)
			}
		case "timeout":
			d, err := ParseLifetime(ScalarString(v))
			if err != nil || d <= 0 {
				return Retirement{}, fmt.Errorf("%s.timeout %s: want a positive duration (10m, 1h)", RetirementKey, v)
			}
			r.Timeout = d
		case "ramp":
			if r.Ramp = strings.TrimSpace(ScalarString(v)); r.Ramp == "" {
				return Retirement{}, fmt.Errorf("%s.ramp %s: want a file path", RetirementKey, v)
			}
		case "ramp_turns":
			n, err := strconv.Atoi(strings.TrimSpace(ScalarString(v)))
			if err != nil || n <= 0 {
				return Retirement{}, fmt.Errorf("%s.ramp_turns %s: want a count of turns, 1 or more", RetirementKey, v)
			}
			r.RampTurns = n
		default:
			return Retirement{}, fmt.Errorf("%s.%s: unknown field; want context_threshold, fallback_tokens, ttl, action, page, timeout, ramp or ramp_turns", RetirementKey, k)
		}
	}
	if r.Action == "" {
		r.Action = RetirementRetire
	}
	if !r.Set() {
		return Retirement{}, fmt.Errorf("%s.context_threshold: required", RetirementKey)
	}
	return r, nil
}

func (r *Retirement) parseThreshold(raw json.RawMessage) error {
	s := strings.TrimSpace(ScalarString(raw))
	bad := fmt.Errorf("%s.context_threshold %s: want input tokens or a percentage of the context window (\"60%%\")", RetirementKey, raw)
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
