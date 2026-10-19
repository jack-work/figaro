package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const hostileText = "denied\x1b]52;c;cGF5bG9hZA==\x07\x1b[2J"

// Whatever an IdP puts in error or error_description reaches the terminal
// inside figaro's error. An escape sequence there could rewrite the screen
// or the clipboard, so it must arrive quoted, never raw.
func assertNoRawControl(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	msg := err.Error()
	assert.NotContains(t, msg, "\x1b")
	assert.NotContains(t, msg, "\x07")
	assert.Contains(t, msg, `\x1b]52`)
}

func TestTheIdPsRefusalOfAPKCESignInArrivesQuoted(t *testing.T) {
	p := newPKCEIdP(t)
	refuse := func(state string) url.Values {
		return url.Values{"error": {hostileText}, "error_description": {hostileText}, "state": {state}}
	}
	_, err := loginPKCE(context.Background(), p.config(t), p.browser(t, refuse), 10*time.Second)
	assertNoRawControl(t, err)
}

func TestATokenEndpointErrorArrivesQuoted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": hostileText, "error_description": hostileText})
	}))
	defer srv.Close()
	_, err := requestToken(srv.Client(), srv.URL, "rou2", url.Values{"grant_type": {"authorization_code"}}, nil)
	assertNoRawControl(t, err)
}

func TestADeviceFlowErrorArrivesQuoted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		if strings.HasSuffix(r.URL.Path, "/device") {
			w.Write([]byte(hostileText))
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"error": hostileText, "error_description": hostileText})
	}))
	defer srv.Close()
	_, err := LoginDeviceCode(DeviceCodeConfig{ClientID: "c", DeviceURL: srv.URL + "/device", TokenURL: srv.URL + "/token"}, func(DevicePrompt) error { return nil })
	assertNoRawControl(t, err)
}

func TestADiscoveryFailureArrivesQuoted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(hostileText))
	}))
	defer srv.Close()
	assertNoRawControl(t, getJSONWith(srv.Client(), srv.URL, &struct{}{}))
}

func TestADeviceFlowPollErrorArrivesQuoted(t *testing.T) {
	for _, body := range []map[string]string{
		{"error": hostileText, "error_description": hostileText},
		{"error": hostileText},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(body)
		}))
		_, _, err := pollDeviceToken(DeviceCodeConfig{ClientID: "c", TokenURL: srv.URL}, "dc", time.Second)
		srv.Close()
		assertNoRawControl(t, err)
	}
}
