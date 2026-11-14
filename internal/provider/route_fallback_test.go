package provider

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestRouteFallbackRejectsUnsafeSignals(t *testing.T) {
	good := `{"error":{"code":"route_fallback","retry_safe":true,"generation":"g1","route":{"model":"vendor/model","api":"messages"},"attempt_token":"private-token"}}`
	for _, tc := range []struct {
		name, body, contentType string
		status                  int
		want                    bool
	}{
		{"valid", good, "application/json", 409, true},
		{"status", good, "application/json", 400, false},
		{"type", good, "text/plain", 409, false},
		{"unsafe", strings.Replace(good, `"retry_safe":true`, `"retry_safe":false`, 1), "application/json", 409, false},
		{"generation", strings.Replace(good, `"generation":"g1",`, "", 1), "application/json", 409, false},
		{"api", strings.Replace(good, `"messages"`, `"chat"`, 1), "application/json", 409, false},
		{"url", strings.Replace(good, `vendor/model`, `https://evil/model`, 1), "application/json", 409, false},
		{"large", good + strings.Repeat(" ", 64<<10), "application/json", 409, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseRouteFallback(tc.status, http.Header{"Content-Type": {tc.contentType}}, []byte(tc.body))
			if (got != nil) != tc.want {
				t.Fatalf("accepted=%v want %v", got != nil, tc.want)
			}
			if got != nil {
				raw, _ := json.Marshal(got)
				if strings.Contains(got.Error(), "private-token") || strings.Contains(string(raw), "private-token") {
					t.Fatal("token exposed")
				}
			}
		})
	}
}
