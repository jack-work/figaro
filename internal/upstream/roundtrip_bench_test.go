package upstream

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/wirelog"
)

var benchStream = strings.Repeat("event: content_block_delta\ndata: {\"type\":\"text_delta\",\"text\":\"hello\"}\n\n", 64)

func benchServer(b *testing.B) *httptest.Server {
	b.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, benchStream)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	b.Cleanup(srv.Close)
	return srv
}

// One provider request as a turn makes it: a POSTed body, a streamed reply
// read in small pieces, through the wire log, over a pooled HTTP/2 connection.
func BenchmarkProviderRoundTrip(b *testing.B) {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	b.Cleanup(func() { slog.SetDefault(old) })
	srv := benchServer(b)
	cfg := Defaults
	cfg.TLS = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	client := &http.Client{Transport: &wirelog.Transport{Inner: New(cfg)}}
	payload := strings.Repeat("x", 16<<10)
	buf := make([]byte, 512)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(payload))
		resp, err := client.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		for {
			_, err := resp.Body.Read(buf)
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
		}
		resp.Body.Close()
	}
}
