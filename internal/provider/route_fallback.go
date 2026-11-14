package provider

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
	"unicode"
)

const AttemptTokenHeader = "Rou2-Attempt-Token"
const FallbackBodyLimit = 64 << 10

type RouteFallback struct {
	Generation string `json:"generation"`
	Model      string `json:"model"`
	API        string `json:"api"`
	token      string
}

func (*RouteFallback) Error() string { return "route fallback requested before execution" }

func ParseRouteFallback(status int, headers http.Header, body []byte) *RouteFallback {
	media, _, err := mime.ParseMediaType(headers.Get("Content-Type"))
	if status != http.StatusConflict || err != nil || media != "application/json" || len(body) > FallbackBodyLimit {
		return nil
	}
	var envelope struct {
		Error struct {
			Code       string `json:"code"`
			RetrySafe  bool   `json:"retry_safe"`
			Generation string `json:"generation"`
			Route      struct {
				Model string `json:"model"`
				API   string `json:"api"`
			} `json:"route"`
			Token string `json:"attempt_token"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return nil
	}
	e := envelope.Error
	if e.Code != "route_fallback" || !e.RetrySafe || e.Generation == "" || len(e.Generation) > 1024 || e.Token == "" || len(e.Token) > 4096 || strings.ContainsAny(e.Token, "\r\n") {
		return nil
	}
	if e.Route.API != "responses" && e.Route.API != "messages" {
		return nil
	}
	if !QualifiedRouteModel(e.Route.Model) {
		return nil
	}
	for _, c := range e.Token {
		if c < 33 || c > 126 {
			return nil
		}
	}
	return &RouteFallback{Generation: e.Generation, Model: e.Route.Model, API: e.Route.API, token: e.Token}
}

func QualifiedRouteModel(model string) bool {
	vendor, id, ok := strings.Cut(model, "/")
	if !ok || vendor == "" || id == "" || len(model) > 1024 || strings.Contains(model, "..") {
		return false
	}
	for _, c := range model {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && !strings.ContainsRune("/-_.", c) {
			return false
		}
	}
	return true
}

type singleAttemptKey struct{}

func WithSingleAttempt(ctx context.Context) context.Context {
	return context.WithValue(ctx, singleAttemptKey{}, true)
}

func SingleAttempt(ctx context.Context) bool {
	single, _ := ctx.Value(singleAttemptKey{}).(bool)
	return single
}

type attemptTokenKey struct{}
type attemptToken struct{ endpoint, token string }

func WithRouteFallback(ctx context.Context, endpoint string, fallback *RouteFallback) context.Context {
	return context.WithValue(ctx, attemptTokenKey{}, attemptToken{endpoint: endpoint, token: fallback.token})
}

func StampAttemptToken(ctx context.Context, req *http.Request) {
	req.Header.Del(AttemptTokenHeader)
	if a, ok := ctx.Value(attemptTokenKey{}).(attemptToken); ok && req.Method == http.MethodPost && req.URL.String() == a.endpoint {
		req.Header.Set(AttemptTokenHeader, a.token)
	}
}
