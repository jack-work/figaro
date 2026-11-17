package routed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

func sentMaxTokens(t *testing.T, knobs provider.Knobs, entry models.Entry, history string) int {
	t.Helper()
	sent := -1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens *int `json:"max_completion_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.MaxTokens != nil {
			sent = *body.MaxTokens
		} else {
			sent = 0
		}
		http.Error(w, "stop here", http.StatusTeapot)
	}))
	defer srv.Close()
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := figLog.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent(history)},
	}}); err != nil {
		t.Fatal(err)
	}
	knobs.Model = "big"
	p, err := Build(provider.BuildContext{
		Knobs: knobs, Resolver: staticToken("k"),
		OpenTranslator: func(aria, ns string) (arialog.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) },
	}, Spec{Name: "max-out", BaseURL: func() string { return srv.URL }, Default: Chat}, "k")
	if err != nil {
		t.Fatal(err)
	}
	p.cat = newCatalogCache(map[string]models.Entry{"big": entry})
	in := provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: knobs.MaxTokens,
		Snapshot: form.FromValue(form.NewValue([]byte(`{"system":{"model":"big"}}`)))}
	_ = p.Send(context.Background(), in, &captureBus{})
	if sent < 0 {
		t.Fatal("no request reached the gateway")
	}
	return sent
}

// #80: the gateway default of 16000 cut a 128k-output model's tool call.
func TestTheDefaultMaxTokensIsTheCatalogMaxOutput(t *testing.T) {
	got := sentMaxTokens(t, provider.Knobs{}, models.Entry{MaxOutput: 128000, Context: models.Context{Default: 1000000}}, "hi")
	if got != 128000 {
		t.Fatalf("max tokens = %d, want the catalog's 128000", got)
	}
}

func TestAnUnknownMaxOutputFallsBackToTheFloor(t *testing.T) {
	got := sentMaxTokens(t, provider.Knobs{}, models.Entry{}, "hi")
	if got != 16000 {
		t.Fatalf("max tokens = %d, want the 16000 floor", got)
	}
}

func TestAnExplicitMaxTokensWins(t *testing.T) {
	got := sentMaxTokens(t, provider.Knobs{MaxTokens: 64}, models.Entry{MaxOutput: 128000}, "hi")
	if got != 64 {
		t.Fatalf("max tokens = %d, want the configured 64", got)
	}
}

// Prompt plus max_tokens past the window is a 400 on most gateways.
func TestTheDefaultLeavesRoomForThePromptInTheWindow(t *testing.T) {
	history := strings.Repeat("abcd", 100000)
	got := sentMaxTokens(t, provider.Knobs{}, models.Entry{MaxOutput: 128000, Context: models.Context{Default: 200000}}, history)
	if got <= 0 || got+100000 > 200000 {
		t.Fatalf("max tokens = %d with a ~100k prompt in a 200k window", got)
	}
	if got < 16000 {
		t.Fatalf("max tokens = %d, below the floor though the window has room", got)
	}
}
