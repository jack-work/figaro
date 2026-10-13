package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The mint request is copied from the muse binary: the header, the bearer and
// the onboard body are what make Meta answer with the subscription key.
func TestMintMuseCodeKeyRequestShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/muse-code/key" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-123" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("x-api-version"); got != "1.0.0" {
			t.Errorf("x-api-version = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var b map[string]any
		if err := json.Unmarshal(body, &b); err != nil || b["onboard"] != true {
			t.Errorf("body = %s", body)
		}
		w.Write([]byte(`{"api_key":" LLM|1|abc ","is_subs_active":true,"user_email":"Gluck@Example.com"}`))
	}))
	defer srv.Close()

	got, err := MintMuseCodeKey(srv.Client(), srv.URL+"/", "access-123")
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "LLM|1|abc" || got.Email != "gluck@example.com" {
		t.Fatalf("got %+v", got)
	}
}

// A lapsed subscription must fail loudly rather than store a key that every
// turn will then bounce off.
func TestMintMuseCodeKeyRefusesInactiveSubscription(t *testing.T) {
	cases := map[string]string{
		"inactive":        `{"api_key":"LLM|1|abc","is_subs_active":false}`,
		"payment needed":  `{"require_payment":true,"action_url":"https://accountscenter.meta.com/muse_code/"}`,
		"no key":          `{}`,
		"http error body": ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if body == "" {
					http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
					return
				}
				w.Write([]byte(body))
			}))
			defer srv.Close()
			if got, err := MintMuseCodeKey(srv.Client(), srv.URL, "t"); err == nil {
				t.Fatalf("minted %+v, want an error", got)
			}
		})
	}
}

func TestMetaDeviceConfigMatchesMuseLauncher(t *testing.T) {
	if metaDeviceConfig.Scope != "" {
		t.Errorf("muse sends no scope; got %q", metaDeviceConfig.Scope)
	}
	if metaDeviceConfig.DeviceURL != "https://auth.meta.com/oidc/device/authorization/" ||
		metaDeviceConfig.TokenURL != "https://auth.meta.com/oidc/device/token/" ||
		metaDeviceConfig.ClientID != "1031625952748946" {
		t.Errorf("config drifted from the muse launcher: %+v", metaDeviceConfig)
	}
}
