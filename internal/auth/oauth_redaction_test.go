package auth

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestOAuthEndpointErrorsNeverReflectSecrets(t *testing.T) {
	const secret = "canary-code-verifier-access-refresh-secret"
	for _, status := range []int{http.StatusBadRequest, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				json.NewEncoder(w).Encode(map[string]any{"error": secret, "error_description": secret, "access_token": secret, "refresh_token": secret, "token_type": secret})
			}))
			defer server.Close()
			_, err := requestTokenContext(context.Background(), server.Client(), server.URL, "public-client", url.Values{"code": {secret}, "code_verifier": {secret}}, nil)
			require.Error(t, err)
			require.NotContains(t, err.Error(), secret)
		})
	}
}

func TestPKCECallbackErrorsNeverReflectSecrets(t *testing.T) {
	const secret = "canary-callback-code-token-verifier-secret"
	redirect, err := url.Parse("http://localhost:1455/auth/callback")
	require.NoError(t, err)
	done := make(chan pkceRedirect, 1)
	request := httptest.NewRequest(http.MethodGet, redirect.String()+"?"+url.Values{"state": {"expected"}, "error": {secret}, "error_description": {secret}, "code": {secret}}.Encode(), nil)
	recorder := httptest.NewRecorder()
	pkceCallback("provider", "expected", redirect, done).ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.NotContains(t, recorder.Body.String(), secret)
	select {
	case result := <-done:
		require.Error(t, result.err)
		require.NotContains(t, result.err.Error(), secret)
	default:
		t.Fatal("callback did not finish")
	}
}
