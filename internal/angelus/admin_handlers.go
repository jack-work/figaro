package angelus

// The daemon's own state: status, the provider round-trip ledger, and the
// bindings it persists across a restart.
//
// Split out of protocol.go, which had grown to 2,011 lines and answered every
// question at once. Same package, same behaviour: only the reader's job
// changes. plans/api-coherence.md step 5.

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/logring"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/wirelog"
)

func (h *handlers) status(ctx context.Context, params json.RawMessage) (interface{}, error) {
	return rpc.StatusResponse{
		Uptime:      h.angelus.StartedAt.UnixMilli(),
		FigaroCount: h.angelus.Registry.FigaroCount(),
		BoundPIDs:   h.angelus.Registry.BoundPIDCount(),
		Build:       h.angelus.Build,
		Mem:         h.angelus.MemStatus(),
	}, nil
}

// providerLedger answers "what did the provider last say to this aria, and is
// anything still in flight".
func (h *handlers) providerLedger(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var req rpc.ProviderLedgerRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
	}

	out := []rpc.ProviderRound{}
	retained := 0
	if ring := figOtel.Recent(); ring != nil {
		entries := ring.Recent(0, func(e logring.Entry) bool { return e.Msg == wirelog.RoundLog })
		retained = len(entries)
		for _, e := range entries {
			r := roundFromLog(e)
			if req.Aria != "" && r.Aria != req.Aria {
				continue
			}
			out = append(out, r)
		}
	}

	for _, f := range wirelog.Outstanding() {
		if req.Aria != "" && f.Aria != req.Aria {
			continue
		}
		out = append(out, rpc.ProviderRound{
			Seq: f.Seq, Aria: f.Aria, Method: f.Method, URL: f.URL,
			StartedAtMS: f.StartedAt.UnixMilli(),
			DurationMS:  f.Age().Milliseconds(),
			ReqBytes:    f.ReqBytes,
			InFlight:    true,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAtMS < out[j].StartedAtMS })

	if req.Limit > 0 && len(out) > req.Limit {
		out = out[len(out)-req.Limit:]
	}
	return rpc.ProviderLedgerResponse{Rounds: out, Retained: retained}, nil
}

// roundFromLog reads back what wirelog wrote. The attribute names are the
// whole contract between the two, and they are the only coupling: nothing
// else in the daemon needs to know this shape.
func roundFromLog(e logring.Entry) rpc.ProviderRound {
	r := rpc.ProviderRound{
		Seq:         e.Seq,
		StartedAtMS: e.Time.UnixMilli(),
		Aria:        logAttrString(e, "aria"),
		Method:      logAttrString(e, "method"),
		URL:         logAttrString(e, "url"),
		RequestID:   logAttrString(e, "request_id"),
		Err:         logAttrString(e, "err"),
		Status:      int(logAttrInt(e, "status")),
		DurationMS:  logAttrInt(e, "duration_ms"),
		ReqBytes:    logAttrInt(e, "req_bytes"),
		RetryAfterS: logAttrInt(e, "retry_after_s"),
	}
	// The record is written when the round-trip FINISHES, so back-date it:
	// callers sort and display by start, which is what lines a refusal up
	// against the turn that provoked it.
	r.StartedAtMS -= r.DurationMS
	for k, v := range e.Attrs {
		name, ok := strings.CutPrefix(k, "ratelimit_")
		if !ok {
			continue
		}
		if r.RateLimit == nil {
			r.RateLimit = map[string]string{}
		}
		r.RateLimit[strings.ReplaceAll(name, "_", "-")], _ = v.(string)
	}
	return r
}

func logAttrString(e logring.Entry, key string) string {
	s, _ := e.Attrs[key].(string)
	return s
}

// logAttrInt tolerates the numeric widths slog hands back: an attr set with an
// int and one set with an int64 are the same fact.
func logAttrInt(e logring.Entry, key string) int64 {
	switch v := e.Attrs[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case uint64:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

func (h *handlers) saveBindings(ctx context.Context, params json.RawMessage) (interface{}, error) {
	path := h.angelus.BindingsPath()
	if err := SaveBindings(h.angelus.Registry, path); err != nil {
		return nil, err
	}
	slog.Info("saved pid bindings", "path", path, "count", h.angelus.Registry.BoundPIDCount())
	return rpc.SaveBindingsResponse{
		OK:    true,
		Count: h.angelus.Registry.BoundPIDCount(),
	}, nil
}
