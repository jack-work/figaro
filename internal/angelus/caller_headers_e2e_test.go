package angelus_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/decide"
	"github.com/jack-work/figaro/internal/provider"
	_ "github.com/jack-work/figaro/internal/provider/routed"
)

type capturedCall struct {
	path string
	id   []string
	node []string
}

// Defends #117 and #110: a test gateway receives both Figaro-Id and
// Figaro-Node on turn requests, inquiry-method decisions, and explicit decide
// calls, overwriting forged headers and honoring [telemetry] node_label.
func TestGatewayReceivesFigaroIdAndNodeAcrossTurnsMethodsAndDecide(t *testing.T) {
	for _, nodeLabel := range []string{"", "cheroot-lab"} {
		name := "store-node-id"
		if nodeLabel != "" {
			name = "configured-node-label"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv(config.DetachedEnv, "")

			var mu sync.Mutex
			var calls []capturedCall
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.Method == http.MethodPost {
					mu.Lock()
					calls = append(calls, capturedCall{
						path: r.URL.Path,
						id:   r.Header.Values(provider.FigaroIDHeader),
						node: r.Header.Values(provider.FigaroNodeHeader),
					})
					mu.Unlock()
				}
				switch r.URL.Path {
				case "/v1/chat/completions":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"+
						"data: {\"id\":\"1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n"+
						"data: [DONE]\n\n")
				case "/v1/systemone":
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"d1","model":"jev-latest","answers":{"decision":{"type":"choice","choice":"high","confidence":0.9,"probabilities":{"low":0.1,"high":0.9}}}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer gw.Close()

			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0o700))
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "providers"), 0o700))

			cfgBody := "default_outfit = \"gw\"\n"
			if nodeLabel != "" {
				cfgBody += fmt.Sprintf("[telemetry]\nnode_label = %q\n", nodeLabel)
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfgBody), 0o600))

			provBody := fmt.Sprintf(`
base_url = "%s/v1"
api_key = "test-gateway-token"
send_figaro_id = true
headers = { "Figaro-Id" = "forged-id", "Figaro-Node" = "forged-node" }
[routing]
default = "chat"
`, gw.URL)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", "gateway.toml"), []byte(provBody), 0o600))

			outfitBody := `
[system]
provider = "gateway"
model = "llama-4"

[system.decide]
provider = "gateway"
model = "jev-latest"

[system.methods.effort]
key = "system.thinking_effort"
event = "inquiry"
keep_cache = false
context = "classify: {{inquiry}}"
options = ["low", "high"]
default = "low"
`
			require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", "gw.toml"), []byte(outfitBody), 0o600))

			a, acli, ctx, _ := daemonWithProvider(t, dir, nil, nil, func(sc *angelus.ServerConfig) {
				sc.ProviderFactory = func(provName string, knobs provider.Knobs) (provider.Provider, error) {
					reg := provider.Lookup(provName)
					if reg == nil {
						return nil, fmt.Errorf("unknown provider %q", provName)
					}
					return reg.Build(provider.BuildContext{
						Loaded: sc.Config,
						Knobs:  knobs,
						OpenTranslator: func(ariaID, ns string) (arialog.Log[[]json.RawMessage], error) {
							return sc.Angelus.Backend.OpenTranslator(ariaID, ns)
						},
					})
				}
				sc.Decider = decide.New(func(provName string) (decide.Target, error) {
					return decide.Target{
						BaseURL:      gw.URL + "/v1",
						SendFigaroID: sc.Config.ProviderSendsFigaroID(provName),
						Headers: func(string) map[string]string {
							return map[string]string{
								"Figaro-Id":   "forged-id",
								"Figaro-Node": "forged-node",
							}
						},
					}, nil
				})
			})

			wantNode := a.Backend.NodeID()
			if nodeLabel != "" {
				wantNode = nodeLabel
			}

			created, err := acli.Create(ctx, dress(t, "gw"), nil)
			require.NoError(t, err)
			ariaID := created.FigaroID

			client := dialAria(t, a, ariaID, func(string, json.RawMessage) {})
			var quaOut rpc.QuaResponse
			require.NoError(t, client.Call(ctx, rpc.MethodQua, rpc.QuaRequest{Text: "hello gateway"}, &quaOut))

			require.Eventually(t, func() bool {
				mu.Lock()
				defer mu.Unlock()
				hasTurn, hasMethod := false, false
				for _, c := range calls {
					if c.path == "/v1/chat/completions" {
						hasTurn = true
					}
					if c.path == "/v1/systemone" {
						hasMethod = true
					}
				}
				return hasTurn && hasMethod
			}, 5*time.Second, 15*time.Millisecond)

			_, err = acli.Decide(ctx, rpc.DecideRequest{
				FigaroID: ariaID,
				Question: "which rung?\n?? low high",
			})
			require.NoError(t, err)

			mu.Lock()
			defer mu.Unlock()
			require.Len(t, calls, 3, "expected method Ask + turn Send + explicit Decide")
			for _, c := range calls {
				require.Equal(t, []string{ariaID}, c.id, "path %s Figaro-Id", c.path)
				require.Equal(t, []string{wantNode}, c.node, "path %s Figaro-Node", c.path)
			}
		})
	}
}
