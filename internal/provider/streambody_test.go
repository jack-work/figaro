package provider_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/provider"
)

// A fault mid-body must abort the request. The failure this forbids is a
// truncated body the far end accepts as a whole conversation.
func TestAMidBodyFaultAbortsTheRequestRatherThanTruncatingIt(t *testing.T) {
	type seen struct {
		body string
		err  error
	}
	got := make(chan seen, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		got <- seen{string(b), err}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	boom := errors.New("the log went away mid-body")
	req, err := http.NewRequest("POST", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := provider.NewRequestBody(func(w io.Writer) error {
		if _, err := io.WriteString(w, `{"messages":[{"role":"user"`); err != nil {
			return err
		}
		return boom
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	rb.Attach(req)

	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("client got a response, want the writer's error")
	}
	if !strings.Contains(err.Error(), boom.Error()) {
		t.Fatalf("client error %v does not carry %v", err, boom)
	}
	if s := <-got; s.err == nil {
		t.Fatalf("server read a complete body %q, want a read error", s.body)
	}
}

// GetBody is what makes a replay possible: without it an HTTP/2 GOAWAY or a
// 307 is a hard failure rather than a second attempt.
func TestTheBodyIsReplayedFromGetBody(t *testing.T) {
	var bodies []string
	var attempts int
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.WriteHeader(http.StatusOK)
	}))
	defer final.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		http.Redirect(w, r, final.URL, http.StatusTemporaryRedirect)
	}))
	defer first.Close()

	req, err := http.NewRequest("POST", first.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := provider.NewRequestBody(func(w io.Writer) error {
		attempts++
		_, err := fmt.Fprintf(w, `{"attempt":%d}`, attempts)
		return err
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	rb.Attach(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if len(bodies) != 2 {
		t.Fatalf("servers saw %d bodies, want 2: %q", len(bodies), bodies)
	}
	if bodies[0] != `{"attempt":1}` || bodies[1] != `{"attempt":2}` {
		t.Fatalf("bodies %q: the replay did not re-walk", bodies)
	}
}

// The pipe's writer must not outlive a request nobody read.
func TestAnAbandonedBodyStopsItsWriter(t *testing.T) {
	stopped := make(chan error, 1)
	req, err := http.NewRequest("POST", "http://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := provider.NewRequestBody(func(w io.Writer) error {
		for {
			if _, err := io.WriteString(w, strings.Repeat("x", 64*1024)); err != nil {
				stopped <- err
				return err
			}
		}
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	rb.Attach(req)
	if err := req.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err == nil {
		t.Fatalf("the writer stopped without an error")
	}
}

// A buffered body and a streamed body are the same bytes, however often the
// request is replayed.
func TestBothFramingsPutTheSameBytesOnTheWire(t *testing.T) {
	write := func(w io.Writer) error {
		_, err := io.WriteString(w, `{"model":"m","messages":[{"role":"user"}]}`)
		return err
	}
	for _, streamed := range []bool{false, true} {
		var got string
		var length int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got, length = string(b), r.ContentLength
			w.WriteHeader(http.StatusOK)
		}))
		req, err := http.NewRequest("POST", srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		rb, err := provider.NewRequestBody(write, streamed)
		if err != nil {
			t.Fatal(err)
		}
		rb.Attach(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("streamed=%v: %v", streamed, err)
		}
		resp.Body.Close()
		srv.Close()
		if want := `{"model":"m","messages":[{"role":"user"}]}`; got != want {
			t.Fatalf("streamed=%v: server saw %q, want %q", streamed, got, want)
		}
		if streamed && length != -1 {
			t.Fatalf("streamed body declared a length of %d", length)
		}
		if !streamed && length != int64(len(got)) {
			t.Fatalf("buffered body declared %d for %d bytes", length, len(got))
		}
	}
}

// The two framings differ in EXACTLY ONE THING on the wire: whether the
// request declares its length. Over HTTP/1.1 that is chunked transfer
// encoding; over HTTP/2 -- which api.anthropic.com, api.openai.com and
// api.githubcopilot.com all negotiate -- it is simply an absent length.
func TestTheFramingsDifferOnlyInTheirLength(t *testing.T) {
	write := func(w io.Writer) error {
		_, err := io.WriteString(w, `{"model":"m"}`)
		return err
	}
	for _, tc := range []struct {
		streamed bool
		chunked  bool
		length   int64
	}{{false, false, 13}, {true, true, -1}} {
		var gotChunked bool
		var gotLength int64
		var gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotBody, gotLength = string(b), r.ContentLength
			for _, e := range r.TransferEncoding {
				if e == "chunked" {
					gotChunked = true
				}
			}
		}))
		req, err := http.NewRequest("POST", srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		rb, err := provider.NewRequestBody(write, tc.streamed)
		if err != nil {
			t.Fatal(err)
		}
		rb.Attach(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("streamed=%v: %v", tc.streamed, err)
		}
		resp.Body.Close()
		srv.Close()
		if gotBody != `{"model":"m"}` {
			t.Fatalf("streamed=%v: body %q", tc.streamed, gotBody)
		}
		if gotChunked != tc.chunked {
			t.Fatalf("streamed=%v: chunked=%v, want %v", tc.streamed, gotChunked, tc.chunked)
		}
		if gotLength != tc.length {
			t.Fatalf("streamed=%v: server saw Content-Length %d, want %d", tc.streamed, gotLength, tc.length)
		}
	}
}
