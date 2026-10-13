package angelus

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

func (h *handlers) memCollect(ctx context.Context, params json.RawMessage) (interface{}, error) {
	return rpc.StatusResponse{
		Uptime:      h.angelus.StartedAt.UnixMilli(),
		FigaroCount: h.angelus.Registry.FigaroCount(),
		BoundPIDs:   h.angelus.Registry.BoundPIDCount(),
		Build:       h.angelus.Build,
		Mem:         h.angelus.MemCollectStatus(),
	}, nil
}

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
			Stream:      streamFromWirelog(f.Stream),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAtMS < out[j].StartedAtMS })

	if req.Limit > 0 && len(out) > req.Limit {
		out = out[len(out)-req.Limit:]
	}
	return rpc.ProviderLedgerResponse{Rounds: out, Retained: retained}, nil
}

// roundFromLog reads what wirelog wrote; the attribute names are the whole contract.
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
	// The record is written when the round-trip finishes; callers sort by start.
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
	r.Stream = streamFromLog(e)
	return r
}

// A record without a stream status is a plain HTTP round-trip and gets no stream block.
func streamFromLog(e logring.Entry) *rpc.StreamStats {
	status := logAttrString(e, "stream_status")
	if status == "" {
		return nil
	}
	s := &rpc.StreamStats{
		Status:            status,
		ErrClass:          logAttrString(e, "stream_err_class"),
		Events:            logAttrInt(e, "stream_events"),
		UnknownEvents:     logAttrInt(e, "stream_unknown_events"),
		RespBytes:         logAttrInt(e, "resp_bytes"),
		FirstEventMS:      logAttrInt(e, "stream_first_event_ms"),
		FirstToolMS:       logAttrInt(e, "stream_first_tool_ms"),
		FirstArgumentMS:   logAttrInt(e, "stream_first_argument_ms"),
		ArgumentDeltas:    logAttrInt(e, "stream_argument_deltas"),
		ArgumentBytes:     logAttrInt(e, "stream_argument_bytes"),
		ArgumentUnmatched: logAttrInt(e, "stream_argument_unmatched"),
		TypesDropped:      logAttrInt(e, "stream_types_dropped"),
	}
	for k := range e.Attrs {
		name, ok := strings.CutPrefix(k, wirelog.StreamEventTypeAttrPrefix)
		if !ok {
			continue
		}
		if s.EventTypes == nil {
			s.EventTypes = map[string]int64{}
		}
		s.EventTypes[name] = logAttrInt(e, k)
	}
	return s
}

func streamFromWirelog(s *wirelog.StreamStats) *rpc.StreamStats {
	if s == nil {
		return nil
	}
	out := &rpc.StreamStats{
		Status:            s.Status,
		ErrClass:          s.ErrClass,
		Events:            s.Events,
		UnknownEvents:     s.UnknownEvents,
		RespBytes:         s.RespBytes,
		FirstEventMS:      s.FirstEventMS,
		FirstToolMS:       s.FirstToolMS,
		FirstArgumentMS:   s.FirstArgumentMS,
		ArgumentDeltas:    s.ArgumentDeltas,
		ArgumentBytes:     s.ArgumentBytes,
		ArgumentUnmatched: s.ArgumentUnmatched,
		TypesDropped:      s.TypesDropped,
	}
	if len(s.EventTypes) > 0 {
		out.EventTypes = make(map[string]int64, len(s.EventTypes))
		for k, v := range s.EventTypes {
			out.EventTypes[k] = v
		}
	}
	return out
}

func logAttrString(e logring.Entry, key string) string {
	s, _ := e.Attrs[key].(string)
	return s
}

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
