package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// Defends #89: the native provider names its figaro only when its file asks.
func TestTheNativeProviderSendsFigaroIdOnlyWhenItsFileAsks(t *testing.T) {
	for name, tc := range map[string]struct {
		file string
		want bool
	}{
		"no file": {"", false},
		"unset":   {"[models.\"claude-test\"]\ncontext = 1000\n", false},
		"false":   {"send_figaro_id = false\n", false},
		"true":    {"send_figaro_id = true\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			loaded := &config.Loaded{ConfigDir: t.TempDir()}
			if tc.file != "" {
				path := loaded.ProviderAuthPath(providerName)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(tc.file), 0o600))
			}
			path, data, err := readProviderFile(loaded)
			require.NoError(t, err)
			sends, err := sendsFigaroID(path, data)
			require.NoError(t, err)
			require.Equal(t, tc.want, sends)

			var got []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				got = r.Header.Values(provider.FigaroIDHeader)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, sseBody(evStart, evText, evDelta("end_turn"), evStop))
			}))
			defer srv.Close()

			be, aria := store.NewTestAria(t, "d", message.Patch{})
			figLog, err := be.OpenFigIR(aria)
			require.NoError(t, err)
			_, err = figLog.Append(store.Entry[message.Message]{Payload: message.Message{
				Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")},
			}})
			require.NoError(t, err)
			rows, err := be.OpenTranslator(aria, "anthropic")
			require.NoError(t, err)
			a, err := New(provider.Knobs{Model: "claude-test", MaxTokens: 64}, nilResolver{},
				func(string) (store.Log[[]json.RawMessage], error) { return rows, nil })
			require.NoError(t, err)
			route := provider.AnthropicDirect()
			route.BaseURL = srv.URL + "/v1"
			a.Route, a.HTTPClient, a.SendFigaroID = route, srv.Client(), sends

			require.NoError(t, a.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &recordingBus{}))
			if tc.want {
				require.Equal(t, []string{aria}, got)
			} else {
				require.Empty(t, got)
			}
		})
	}
}

// El Inspector XIV nit (b): a bad provider file is named by path and line,
// never by the decoder's message, which can quote the line's value.
func TestABadProviderFileNeverQuotesItsValues(t *testing.T) {
	const secret = "sk-ant-PRIVATE-TOKEN-VALUE"
	for name, doc := range map[string]string{
		"syntax":     "api_key = \"" + secret + "\"\nsend_figaro_id = " + secret + "\n",
		"wrong type": "api_key = \"x\"\nsend_figaro_id = \"" + secret + "\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := sendsFigaroID("/p/anthropic.toml", []byte(doc))
			require.Error(t, err)
			require.NotContains(t, err.Error(), secret)
			require.Contains(t, err.Error(), "/p/anthropic.toml")
			if _, err = catalogFrom("/p/anthropic.toml", []byte(doc)); err != nil {
				require.NotContains(t, err.Error(), secret)
			}
		})
	}
}
