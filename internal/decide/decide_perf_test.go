package decide

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
)

func BenchmarkPerfDecideOverhead2000(b *testing.B) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(prev)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, liveNoul)
	}))
	defer srv.Close()
	msgs := make([]message.Message, 0, 2000)
	for i := 0; i < 2000; i++ {
		msgs = append(msgs, say(message.RoleOutput, fmt.Sprintf("%04d %s", i, strings.Repeat("tool output ", 40))))
	}
	t := &testing.T{}
	log := logOf(t, msgs...)
	e := at(srv.URL)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Decide(context.Background(), Source{Log: log, Defaults: jevDefaults}, rpc.DecideRequest{Question: "is it?"}); err != nil {
			b.Fatal(err)
		}
	}
}
