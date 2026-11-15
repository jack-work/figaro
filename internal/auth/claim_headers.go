package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
)

func (s OAuthSettings) LoginHeaders(idToken string) (map[string]string, error) {
	if len(s.ClaimHeaders) == 0 {
		return nil, nil
	}
	refuse := errors.New("oauth: required login account metadata is missing or invalid")
	if len(s.ClaimHeaders) > 8 || len(idToken) > 65536 {
		return nil, refuse
	}
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return nil, refuse
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, refuse
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(payload, &claims) != nil {
		return nil, refuse
	}
	var issuer string
	var expiry int64
	if json.Unmarshal(claims["iss"], &issuer) != nil || strings.TrimRight(issuer, "/") != strings.TrimRight(s.Issuer, "/") || json.Unmarshal(claims["exp"], &expiry) != nil || expiry <= time.Now().Unix() {
		return nil, refuse
	}
	var audience string
	var audiences []string
	matched := json.Unmarshal(claims["aud"], &audience) == nil && audience == s.ClientID
	if !matched && json.Unmarshal(claims["aud"], &audiences) == nil {
		for _, a := range audiences {
			matched = matched || a == s.ClientID
		}
	}
	if !matched {
		return nil, refuse
	}
	headers := map[string]string{}
	for name, path := range s.ClaimHeaders {
		canonical := http.CanonicalHeaderKey(name)
		if !httpguts.ValidHeaderFieldName(name) || len(path) == 0 || len(path) > 8 {
			return nil, refuse
		}
		switch strings.ToLower(name) {
		case "authorization", "proxy-authorization", "cookie", "set-cookie", "host", "content-length", "content-type", "accept", "connection", "transfer-encoding", "figaro-id", "rou2-lease", "rou2-attempt-token":
			return nil, refuse
		}
		if _, exists := headers[canonical]; exists {
			return nil, refuse
		}
		current := claims
		var value string
		for i, key := range path {
			raw, ok := current[key]
			if !ok {
				return nil, refuse
			}
			if i == len(path)-1 {
				if json.Unmarshal(raw, &value) != nil {
					return nil, refuse
				}
			} else {
				if json.Unmarshal(raw, &current) != nil {
					return nil, refuse
				}
			}
		}
		if len(value) < 1 || len(value) > 128 {
			return nil, refuse
		}
		for _, c := range value {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return nil, refuse
			}
		}
		headers[canonical] = value
	}
	return headers, nil
}
