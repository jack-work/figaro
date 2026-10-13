package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// RFC 8628 servers answer the pending poll with HTTP 400 and an error, where
// GitHub answers 200. Both must keep polling, and slow_down with no
// interval must back off rather than hammer.
func TestDeviceFlowPollsThroughStandardErrors(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.URL.Path {
		case "/device":
			if r.Form.Get("scope") != "" {
				t.Errorf("scope sent when none configured: %q", r.Form.Get("scope"))
			}
			w.Write([]byte(`{"device_code":"dc","user_code":"ABCD-EFGH","verification_uri":"https://x/device","verification_uri_complete":"https://x/device?code=ABCD-EFGH","interval":0,"expires_in":60}`))
		case "/token":
			if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || r.Form.Get("device_code") != "dc" {
				t.Errorf("token form = %v", r.Form)
			}
			switch polls.Add(1) {
			case 1:
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"authorization_pending"}`))
			default:
				w.Write([]byte(`{"access_token":"granted"}`))
			}
		}
	}))
	defer srv.Close()

	cfg := DeviceCodeConfig{ClientID: "c", DeviceURL: srv.URL + "/device", TokenURL: srv.URL + "/token"}
	tok, next, err := pollDeviceToken(cfg, "dc", time.Second)
	if err != nil || tok != "" || next != time.Second {
		t.Fatalf("pending poll: tok=%q next=%v err=%v", tok, next, err)
	}
	tok, _, err = pollDeviceToken(cfg, "dc", time.Second)
	if err != nil || tok != "granted" {
		t.Fatalf("granted poll: tok=%q err=%v", tok, err)
	}

	resp, err := postDeviceForm(cfg, cfg.DeviceURL, url.Values{"client_id": {"c"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestDeviceFlowSlowDownBacksOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"slow_down"}`))
	}))
	defer srv.Close()
	_, next, err := pollDeviceToken(DeviceCodeConfig{TokenURL: srv.URL}, "dc", 5*time.Second)
	if err != nil || next != 10*time.Second {
		t.Fatalf("next=%v err=%v, want 10s", next, err)
	}
}

func TestDeviceFlowSurfacesDenial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"access_denied","error_description":"user said no"}`))
	}))
	defer srv.Close()
	_, _, err := pollDeviceToken(DeviceCodeConfig{TokenURL: srv.URL}, "dc", time.Second)
	if err == nil || !strings.Contains(err.Error(), "user said no") {
		t.Fatalf("err = %v", err)
	}
}
