package angelus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/wirelog"
)

// A websocket never passes through http.RoundTripper, so unless the ledger
// reads wirelog's stream attrs an aria talking over a socket reads as idle.
func TestProviderLedgerCarriesAFinishedStream(t *testing.T) {
	installRing(t)

	s := wirelog.BeginStream(context.Background(), "94f0752b", wirelog.StreamMethodWS,
		"wss://api.example.com/v1/stream?token=secret",
		wirelog.WithEventVocabulary("response.created", "response.function_call_arguments.delta"))
	s.RequestBytes(8192)
	s.Event("response.created", 100)
	s.Event("response.function_call_arguments.delta", 60)
	s.ToolStart()
	s.ArgumentDelta(40, true)
	s.ArgumentDelta(10, false)
	s.Event("response.something_new", 20)
	s.UnknownEvent()
	s.Finish("incomplete", context.Canceled)

	h := &handlers{}
	params, _ := json.Marshal(rpc.ProviderLedgerRequest{Aria: "94f0752b"})
	out, err := h.providerLedger(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(rpc.ProviderLedgerResponse)
	if len(got.Rounds) != 1 {
		t.Fatalf("want 1 round, got %d", len(got.Rounds))
	}
	r := got.Rounds[0]
	if r.Method != wirelog.StreamMethodWS {
		t.Errorf("method = %q", r.Method)
	}
	if strings.Contains(r.URL, "secret") {
		t.Fatalf("query survived onto the wire: %q", r.URL)
	}
	if r.Stream == nil {
		t.Fatal("no stream block: a streaming attempt is indistinguishable from an HTTP one")
	}
	st := r.Stream
	if st.Status != "incomplete" || st.ErrClass != "canceled" {
		t.Errorf("terminal outcome lost: status=%q err_class=%q", st.Status, st.ErrClass)
	}
	if st.Events != 3 || st.RespBytes != 180 {
		t.Errorf("event accounting lost: %+v", st)
	}
	if st.UnknownEvents != 1 {
		t.Errorf("unknown_events = %d", st.UnknownEvents)
	}
	if st.ArgumentDeltas != 2 || st.ArgumentBytes != 50 || st.ArgumentUnmatched != 1 {
		t.Errorf("argument accounting lost: %+v", st)
	}
	if st.EventTypes["response.created"] != 1 {
		t.Errorf("per-type counters lost: %v", st.EventTypes)
	}
	if r.Err != "" {
		t.Errorf("a stream must never put error text on the wire: %q", r.Err)
	}
}

// An open stream is current state; the record only exists once it is over,
// and "it has been open two minutes with no events" is the incident.
func TestProviderLedgerShowsARunningStream(t *testing.T) {
	installRing(t)

	s := wirelog.BeginStream(context.Background(), "abcd1234", wirelog.StreamMethodWS, "wss://api.example.com/v1/stream")
	s.RequestBytes(1024)
	s.Event("response.created", 90)
	defer s.Finish("ok", nil)

	h := &handlers{}
	params, _ := json.Marshal(rpc.ProviderLedgerRequest{Aria: "abcd1234"})
	out, _ := h.providerLedger(context.Background(), params)
	got := out.(rpc.ProviderLedgerResponse)
	if len(got.Rounds) != 1 {
		t.Fatalf("want the running stream, got %d rounds", len(got.Rounds))
	}
	r := got.Rounds[0]
	if !r.InFlight {
		t.Error("a running stream must read as in flight")
	}
	if r.ReqBytes != 1024 {
		t.Errorf("req_bytes = %d", r.ReqBytes)
	}
	if r.Stream == nil || r.Stream.Events != 1 || r.Stream.RespBytes != 90 {
		t.Fatalf("live counters missing: %+v", r.Stream)
	}
	if r.Stream.Status != "" {
		t.Errorf("a running stream has no terminal status yet, got %q", r.Stream.Status)
	}
}

// HTTP rounds must not sprout an empty stream block; the JSON for a
// request/response call is unchanged by this feature.
func TestProviderLedgerLeavesHTTPRoundsWithoutAStreamBlock(t *testing.T) {
	installRing(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: &wirelog.Transport{Inner: http.DefaultTransport}}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req = req.WithContext(wirelog.WithAria(req.Context(), "aaaa1111"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	h := &handlers{}
	params, _ := json.Marshal(rpc.ProviderLedgerRequest{Aria: "aaaa1111"})
	out, _ := h.providerLedger(context.Background(), params)
	got := out.(rpc.ProviderLedgerResponse)
	if len(got.Rounds) != 1 {
		t.Fatalf("want 1 round, got %d", len(got.Rounds))
	}
	if got.Rounds[0].Stream != nil {
		t.Errorf("an HTTP round-trip grew a stream block: %+v", got.Rounds[0].Stream)
	}
	blob, _ := json.Marshal(got.Rounds[0])
	if strings.Contains(string(blob), "stream") {
		t.Errorf("stream key present in HTTP round JSON: %s", blob)
	}
}
