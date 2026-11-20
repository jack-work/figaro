package routed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

const messagesReply = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"back\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func restartingGateway(t *testing.T, down int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if int(calls.Add(1)) <= down {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, "error code: 502")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, messagesReply)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestATurnSurvivesTheGatewayRestartingUnderIt(t *testing.T) {
	srv, calls := restartingGateway(t, 1)
	if err := sendWithFirstByte(t, srv, "messages", "5s"); err != nil {
		t.Fatalf("one 502 failed the turn: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("%d requests, want the 502 and its retry", calls.Load())
	}

	srv, calls = restartingGateway(t, 2)
	err := sendWithFirstByte(t, srv, "messages", "5s")
	if err == nil {
		t.Fatal("two 502s in a row completed the turn")
	}
	if calls.Load() != 2 {
		t.Fatalf("%d requests, want exactly one retry", calls.Load())
	}
}

func TestGatewayRetryIsOnForAGatewayAndAFileCanTurnItOff(t *testing.T) {
	on := gatewayAt(t, "retryon", "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\n")
	off := gatewayAt(t, "retryoff", "preset = \"gateway\"\nbase_url = \"http://127.0.0.1:1/v1\"\nretry_gateway = false\n")
	if !on.spec.RetryGateway || off.spec.RetryGateway {
		t.Fatalf("on=%v off=%v", on.spec.RetryGateway, off.spec.RetryGateway)
	}
}
