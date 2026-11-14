package routed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

func TestNativeFallback(t *testing.T) {
	for _, from := range []Transport{Responses, Messages} {
		for _, scenario := range []string{"success", "unknown-context", "stale", "wrong-model", "wrong-api", "second", "ordinary", "unsafe", "malformed", "cancelled", "stream-text", "stream-tool", "lease", "reasoning", "context", "tools", "tools-supported", "image", "images-supported", "redirect", "unauthorized"} {
			t.Run(string(from)+"/"+scenario, func(t *testing.T) {
				to := Messages
				if from == Messages {
					to = Responses
				}
				const alias = "pair@auto"
				target := "vendor/concrete"
				const secret = "canary-attempt-token-NEVER-LOG"
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var hits []hit
				var deadline bool
				var server *httptest.Server
				server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						Model string `json:"model"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					hits = append(hits, hit{path: r.URL.Path, model: body.Model, headers: r.Header.Clone()})
					if len(hits) == 1 {
						if scenario == "cancelled" {
							cancel()
						}
						if scenario == "unauthorized" {
							http.Error(w, "unauthorized", http.StatusUnauthorized)
							return
						}
						if scenario == "ordinary" {
							http.Error(w, "not quota", 500)
							return
						}
						if scenario == "redirect" {
							http.Redirect(w, r, server.URL+"/other", 307)
							return
						}
						if strings.HasPrefix(scenario, "stream-") {
							w.Header().Set("Content-Type", "text/event-stream")
							if from == Responses {
								if scenario == "stream-tool" {
									io.WriteString(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call1\",\"name\":\"bash\",\"arguments\":\"\"}}\n\n")
								} else {
									io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n")
								}
							} else {
								io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
								if scenario == "stream-tool" {
									io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call1\",\"name\":\"bash\",\"input\":{}}}\n\n")
								} else {
									io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
								}
							}
							return
						}
					}
					if len(hits) == 1 || scenario == "second" {
						generation, model, api, safe := "g1", target, string(to), true
						if scenario == "stale" {
							generation = "g2"
						}
						if scenario == "wrong-model" {
							model = "attacker/model"
						}
						if scenario == "wrong-api" {
							api = string(from)
						}
						if scenario == "unsafe" {
							safe = false
						}
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(409)
						if scenario == "malformed" {
							fmt.Fprintf(w, `{"error":%q`, secret)
							return
						}
						fmt.Fprintf(w, `{"error":{"code":"route_fallback","retry_safe":%t,"generation":%q,"route":{"model":%q,"api":%q},"attempt_token":%q}}`, safe, generation, model, api, secret)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if to == Messages {
						io.WriteString(w, messagesSSE)
					} else {
						io.WriteString(w, responsesSSE)
					}
				}))
				defer server.Close()
				be, aria := store.NewTestAria(t, "d", message.Patch{})
				log, err := be.OpenFigIR(aria)
				if err != nil {
					t.Fatal(err)
				}
				content := message.TextContent("hello")
				if scenario == "image" || scenario == "images-supported" {
					content = message.ImageContent("image/png", "aGVsbG8=")
				}
				_, err = log.Append(store.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{content}}})
				if err != nil {
					t.Fatal(err)
				}
				lease := ""
				if scenario == "lease" {
					lease = "box"
				}
				p, err := New(Spec{Name: "native-" + scenario, BaseURL: func() string { return server.URL }, Default: from, SendFigaroID: true}, provider.Knobs{Model: alias, MaxTokens: 64, Lease: lease}, staticToken("auth-token"), func(a, ns string) (store.Log[[]json.RawMessage], error) { return be.OpenTranslator(a, ns) })
				if err != nil {
					t.Fatal(err)
				}
				capacity := 100000
				if scenario == "context" {
					capacity = 1
				}
				candidate := models.RouteCandidate{Model: target, APIs: []string{string(to)}, ContextLength: &capacity}
				if scenario == "unknown-context" {
					candidate.ContextLength = nil
				}
				p.cat = newCatalogCache(map[string]models.Entry{alias: {Auto: &models.AutoRoute{Generation: "g1", Candidates: []models.RouteCandidate{candidate}}}})
				if scenario == "tools-supported" || scenario == "images-supported" {
					yes := true
					p.cat.entries[target] = models.Entry{Tools: &yes, Images: &yes}
				}
				dir := t.TempDir()
				snap := form.FromMap(map[string]json.RawMessage{"system.model": json.RawMessage(`"pair@auto"`)})
				rawDir, _ := json.Marshal(dir)
				snap = snap.SetPath("system.environment.figaro_wire_dir", rawDir)
				if scenario == "reasoning" {
					snap = snap.SetPath("system.thinking_effort", json.RawMessage(`"high"`))
				}
				in := provider.SendInput{AriaID: aria, FigLog: log, Snapshot: snap, MaxTokens: 64}
				if scenario == "tools" || scenario == "tools-supported" {
					in.Tools = []provider.Tool{{Name: "bash"}}
				}
				ctx, deadlineCancel := context.WithTimeout(ctx, 30_000_000_000)
				defer deadlineCancel()
				_, deadline = ctx.Deadline()
				err = p.Send(ctx, in, &captureBus{})
				wantHits := 1
				wantOK := scenario == "success" || scenario == "unknown-context" || scenario == "tools-supported" || scenario == "images-supported"
				if wantOK || scenario == "second" {
					wantHits = 2
				}
				if scenario == "lease" {
					wantHits = 0
				}
				if len(hits) != wantHits {
					t.Fatalf("hits=%d want %d; err=%v", len(hits), wantHits, err)
				}
				if (err == nil) != wantOK {
					t.Fatalf("err=%v want success=%v", err, wantOK)
				}
				if !deadline {
					t.Fatal("fixture lacks parent deadline")
				}
				if len(hits) > 0 && hits[0].headers.Get(provider.AttemptTokenHeader) != "" {
					t.Fatal("token on primary")
				}
				if len(hits) > 1 {
					h := hits[1]
					if h.path != "/"+string(to) || h.model != target || h.headers.Get(provider.AttemptTokenHeader) != secret || h.headers.Get("Authorization") != "Bearer auth-token" || h.headers.Get(provider.FigaroIDHeader) != aria || h.headers.Get(provider.LeaseHeader) != "" {
						t.Fatalf("bad fallback identity/model/path: %s %s", h.path, h.model)
					}
				}
				if snapshotString(in.Snapshot, "system.model") != alias || p.model != alias {
					t.Fatal("persisted auto model changed")
				}
				if err != nil && strings.Contains(err.Error(), secret) {
					t.Fatal("token in error")
				}
				filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
					if walkErr != nil {
						return walkErr
					}
					if !d.IsDir() {
						data, e := os.ReadFile(path)
						if e != nil {
							return e
						}
						if strings.Contains(string(data), secret) {
							t.Errorf("token leaked in %s", path)
						}
					}
					return nil
				})
			})
		}
	}
}
