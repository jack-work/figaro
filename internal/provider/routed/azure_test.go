package routed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

func loadedWith(t *testing.T, azureTOML string) *config.Loaded {
	t.Helper()
	dir := t.TempDir()
	if azureTOML != "" {
		if err := os.MkdirAll(filepath.Join(dir, "providers"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "providers", "azure.toml"), []byte(azureTOML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &config.Loaded{ConfigDir: dir}
}

// The registration is what the credential chain reads: HasOAuth is the one
// bit that makes BuildResolver consult hush for the Entra token, and the
// env var is the API-key alternative Foundry also accepts as a bearer.
func TestAzureIsAnOAuthProvider(t *testing.T) {
	reg := provider.Lookup("azure")
	if reg == nil {
		t.Fatal("azure is not registered")
	}
	if !reg.HasOAuth {
		t.Fatal("azure without HasOAuth never reads the Entra token hush holds")
	}
	if reg.ExchangeGrant != "" {
		t.Fatal("azure is not an exchange: hush's token IS the bearer")
	}
	if reg.EnvVar != "AZURE_OPENAI_API_KEY" {
		t.Fatalf("env = %q", reg.EnvVar)
	}
	if !strings.Contains(reg.LoginHint, "figaro provider login azure") {
		t.Fatalf("hint = %q", reg.LoginHint)
	}
}

func TestAzureNeedsAnEndpoint(t *testing.T) {
	t.Setenv("AZURE_BASE_URL", "")
	_, err := provider.Lookup("azure").Build(provider.BuildContext{Loaded: loadedWith(t, ""), Resolver: staticToken("t")})
	if err == nil || !strings.Contains(err.Error(), "base_url") || !strings.Contains(err.Error(), "openai/v1") {
		t.Fatalf("err = %v: a missing endpoint must say what one looks like", err)
	}
}

func TestAzureSendsTheResolvedTokenAsABearer(t *testing.T) {
	var got http.Header
	var path, model string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, path = r.Header.Clone(), r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &body)
		model = body.Model
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, chatSSE)
	}))
	defer srv.Close()

	t.Setenv("AZURE_BASE_URL", "")
	loaded := loadedWith(t, `base_url = "`+srv.URL+`/openai/v1/"`+"\ntenant = \"contoso.onmicrosoft.com\"\n")
	be, aria := store.NewTestAria(t, "az", message.Patch{})
	p, err := provider.Lookup("azure").Build(provider.BuildContext{
		Loaded:   loaded,
		Knobs:    provider.Knobs{Model: "gpt-5-deployment", MaxTokens: 64},
		Resolver: staticToken("eyJ0eXAiOiJKV1Qi"),
		Backend:  be,
	})
	if err != nil {
		t.Fatal(err)
	}
	figLog, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("hello")},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := p.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &captureBus{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if path != "/openai/v1/chat/completions" {
		t.Fatalf("path = %s", path)
	}
	if got.Get("Authorization") != "Bearer eyJ0eXAiOiJKV1Qi" {
		t.Fatalf("Authorization = %q", got.Get("Authorization"))
	}
	if model != "gpt-5-deployment" {
		t.Fatalf("model = %q: on Azure the model is the deployment name, sent as given", model)
	}
}

func TestAzureEnvOverridesTheFile(t *testing.T) {
	t.Setenv("AZURE_BASE_URL", "https://override.openai.azure.com/openai/v1")
	p, err := provider.Lookup("azure").Build(provider.BuildContext{
		Loaded:   loadedWith(t, `base_url = "https://file.openai.azure.com/openai/v1"`),
		Resolver: staticToken("t"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if b := p.(*Provider).spec.BaseURL(); b != "https://override.openai.azure.com/openai/v1" {
		t.Fatalf("base = %s", b)
	}
}
