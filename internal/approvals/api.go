package approvals

import (
	"crypto/hmac"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type apiLine struct {
	Op      string          `json:"op"`
	Key     string          `json:"key"`
	Current json.RawMessage `json:"current,omitempty"`
	New     json.RawMessage `json:"new,omitempty"`
}

type apiRequest struct {
	Filing       string    `json:"filing"`
	ID           string    `json:"id"`
	Verb         string    `json:"verb"`
	Target       string    `json:"target"`
	Reason       string    `json:"reason"`
	Expires      string    `json:"expires"`
	State        string    `json:"state"`
	Refusal      string    `json:"refusal,omitempty"`
	Hash         string    `json:"hash,omitempty"`
	Version      uint64    `json:"version"`
	Token        string    `json:"token"`
	Applied      bool      `json:"applied"`
	AtSuccession bool      `json:"at_succession"`
	Lines        []apiLine `json:"lines"`
	Link         string    `json:"link"`
	Raw          string    `json:"raw,omitempty"`
}

type apiDecision struct {
	Hash    string `json:"hash"`
	Version uint64 `json:"version"`
	Token   string `json:"token"`
}

type apiResult struct {
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

func rawOrNull(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	return json.RawMessage(b)
}

func (s *Server) apiRows(r *http.Request) ([]apiRequest, error) {
	rows, err := s.rows(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]apiRequest, 0, len(rows))
	for _, row := range rows {
		a := apiRequest{
			Filing: row.Filing, ID: row.ID, Verb: row.Request.Verb, Target: row.Request.Target,
			Reason: row.Request.Reason, Expires: row.Request.Expires, State: row.Request.State,
			Refusal: row.Refusal, Hash: row.Hash, Version: row.Version, Token: row.Token,
			Applied: row.Applied, AtSuccession: row.AtSuccession, Link: row.Link, Raw: row.Raw,
			Lines: make([]apiLine, 0, len(row.Lines)),
		}
		for _, l := range row.Lines {
			a.Lines = append(a.Lines, apiLine{Op: l.Op, Key: l.Key, Current: rawOrNull([]byte(l.Current)), New: rawOrNull([]byte(l.New))})
		}
		out = append(out, a)
	}
	return out, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) apiList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.apiRows(r)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiResult{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": rows, "admin": s.Admin})
}

func (s *Server) apiOne(w http.ResponseWriter, r *http.Request) {
	rows, err := s.apiRows(r)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiResult{Error: err.Error()})
		return
	}
	for _, a := range rows {
		if a.Filing == r.PathValue("filing") && a.ID == r.PathValue("id") {
			writeJSON(w, http.StatusOK, a)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, apiResult{Error: "no such request: it was decided, withdrawn, or never filed"})
}

func (s *Server) apiDecide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mt := strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]); mt != "application/json" {
			writeJSON(w, http.StatusUnsupportedMediaType, apiResult{Error: "send application/json"})
			return
		}
		var d apiDecision
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&d); err != nil {
			writeJSON(w, http.StatusBadRequest, apiResult{Error: "bad json"})
			return
		}
		filing, id := r.PathValue("filing"), r.PathValue("id")
		if !hmac.Equal([]byte(d.Token), []byte(s.token(filing, id, d.Hash, d.Version))) {
			writeJSON(w, http.StatusForbidden, apiResult{Error: "stale or forged token: reload"})
			return
		}
		if !s.knows(filing) {
			writeJSON(w, http.StatusBadRequest, apiResult{Error: "unknown filing form"})
			return
		}
		dec := Decision{Approver: s.Admin, Filing: filing, ID: id, Hash: d.Hash, Version: d.Version}
		var (
			result string
			err    error
		)
		if approve {
			result, err = s.Decider.Approve(r.Context(), dec)
		} else {
			result, err = s.Decider.Deny(r.Context(), dec)
		}
		if err != nil {
			writeJSON(w, http.StatusConflict, apiResult{Result: result, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, apiResult{Result: result})
	}
}
