package workerpool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Caller interface {
	Open(ctx context.Context, id, class string) error
	Close(ctx context.Context, id string) error
}

func CallerID(pool, box string) string { return pool + "/" + box }

var ErrCallerClosed = errors.New("the rou2 lease is closed and never reopens")

const (
	callerTimeout   = 10 * time.Second
	callerReplyMax  = 64 << 10
	leaseClosedType = "lease_closed"
	leaseUnknown    = "lease_unknown"
)

func LeaseAPI(root, providerBase string) (string, error) {
	r, err := url.Parse(root)
	if err != nil {
		return "", fmt.Errorf("rou2_url %q: %w", root, err)
	}
	b, err := url.Parse(providerBase)
	if err != nil {
		return "", fmt.Errorf("the pool's provider base %q: %w", providerBase, err)
	}
	if r.Scheme != b.Scheme || r.Host != b.Host {
		return "", fmt.Errorf("rou2_url %s is not where the pool's provider sends its credential (%s://%s); the lease API is asked with that credential, so it goes nowhere else", root, b.Scheme, b.Host)
	}
	return strings.TrimRight(root, "/") + "/v1", nil
}

type Bearer interface {
	Resolve() (string, error)
	Invalidate(token string) error
}

type Rou2 struct {
	Base   string
	Bearer Bearer
	HTTP   *http.Client
}

type rou2Reply struct {
	ID    string `json:"id"`
	Class string `json:"class"`
	Error struct {
		Type string `json:"type"`
	} `json:"error"`
}

func (r Rou2) do(ctx context.Context, method, path string, body []byte) (int, rou2Reply, error) {
	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, callerTimeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		token, err := r.Bearer.Resolve()
		if err != nil {
			return 0, rou2Reply{}, fmt.Errorf("the pool's rou2 credential: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.Base, "/")+path, bytes.NewReader(body))
		if err != nil {
			return 0, rou2Reply{}, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, rou2Reply{}, err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, callerReplyMax))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 && r.Bearer.Invalidate(token) == nil {
			continue
		}
		var reply rou2Reply
		_ = json.Unmarshal(raw, &reply)
		return resp.StatusCode, reply, nil
	}
}

func (r Rou2) Open(ctx context.Context, id, class string) error {
	body, _ := json.Marshal(map[string]string{"id": id, "class": class})
	status, reply, err := r.do(ctx, http.MethodPost, "/leases", body)
	switch {
	case err != nil:
		return fmt.Errorf("open rou2 lease %s: %w", id, err)
	case reply.Error.Type == leaseClosedType:
		return fmt.Errorf("open rou2 lease %s: %w", id, ErrCallerClosed)
	case status != http.StatusOK && status != http.StatusCreated:
		return fmt.Errorf("open rou2 lease %s: rou2 answered %d %s", id, status, reply.Error.Type)
	case reply.ID != id || reply.Class != class:
		return fmt.Errorf("open rou2 lease %s of class %s: rou2 answered for %q of class %q", id, class, reply.ID, reply.Class)
	}
	return nil
}

func (r Rou2) Close(ctx context.Context, id string) error {
	parts := strings.Split(id, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	status, reply, err := r.do(ctx, http.MethodDelete, "/leases/"+strings.Join(parts, "/"), nil)
	switch {
	case err != nil:
		return fmt.Errorf("close rou2 lease %s: %w", id, err)
	case status == http.StatusOK || status == http.StatusNoContent:
		return nil
	case reply.Error.Type == leaseClosedType || reply.Error.Type == leaseUnknown:
		return nil
	}
	return fmt.Errorf("close rou2 lease %s: rou2 answered %d %s", id, status, reply.Error.Type)
}
