package provider

import (
	"context"
	"net/http"
	"testing"
)

func TestAttemptTokenIsBoundToOneEndpoint(t *testing.T) {
	fallback := ParseRouteFallback(409, http.Header{"Content-Type": {"application/json"}}, []byte(`{"error":{"code":"route_fallback","retry_safe":true,"generation":"g","route":{"model":"vendor/model","api":"messages"},"attempt_token":"private-token"}}`))
	ctx := WithRouteFallback(context.Background(), "https://gateway/v1/messages", fallback)
	for _, tc := range []struct {
		method, url string
		want        bool
	}{
		{"POST", "https://gateway/v1/messages", true},
		{"POST", "https://attacker/v1/messages", false},
		{"POST", "https://gateway/v1/models", false},
		{"GET", "https://gateway/v1/messages", false},
		{"POST", "https://gateway/v1/messages?target=elsewhere", false},
	} {
		req, err := http.NewRequest(tc.method, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(AttemptTokenHeader, "static-token")
		StampAttemptToken(ctx, req)
		if got := req.Header.Get(AttemptTokenHeader); (got == "private-token") != tc.want || (!tc.want && got != "") {
			t.Fatalf("%s %s leaked or lost token", tc.method, tc.url)
		}
	}
}
