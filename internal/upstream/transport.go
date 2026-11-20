package upstream

import (
	"cmp"
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"
)

type Config struct {
	SendPingTimeout time.Duration
	PingTimeout     time.Duration
	Stall           time.Duration
	MaxAge          time.Duration
	Sweep           time.Duration
	WatchNetwork    bool
	GatewayBackoff  time.Duration
	Dial            func(ctx context.Context, network, addr string) (net.Conn, error)
	TLS             *tls.Config
}

var Defaults = Config{
	SendPingTimeout: 15 * time.Second,
	PingTimeout:     10 * time.Second,
	Stall:           90 * time.Second,
	MaxAge:          15 * time.Minute,
	WatchNetwork:    true,
}

type Transport struct {
	backoff time.Duration
	base    *http.Transport
	stall   time.Duration
	clock   clock
	pool    *pool
}

func New(cfg Config) *Transport {
	dial := cfg.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	p := newPool(cfg)
	t := &Transport{
		backoff: cmp.Or(cfg.GatewayBackoff, DefaultGatewayRetryBackoff),
		stall:   cfg.Stall,
		clock:   wallClock{},
		pool:    p,
		base: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           p.dialer(dial),
			TLSClientConfig:       cfg.TLS,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
			HTTP2: &http.HTTP2Config{
				SendPingTimeout: cfg.SendPingTimeout,
				PingTimeout:     cfg.PingTimeout,
			},
		},
	}
	p.closeIdle = t.base.CloseIdleConnections
	return t
}

const DefaultGatewayRetryBackoff = 2 * time.Second

const ServedHeader = "X-Rou2-Upstream"

type gatewayRetryKey struct{}

func WithGatewayRetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, gatewayRetryKey{}, true)
}

func retriesGateway(req *http.Request) bool {
	on, _ := req.Context().Value(gatewayRetryKey{}).(bool)
	return on
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.roundTripOnce(req)
	if err != nil || !gatewayDown(resp.StatusCode) || !retriesGateway(req) || resp.Header.Get(ServedHeader) != "" || upgrading(req) {
		return resp, err
	}
	retry, ok := replay(req)
	if !ok {
		return resp, nil
	}
	slog.Warn("gateway answered before any output; retrying the request once",
		"host", req.URL.Host, "status", resp.StatusCode, "backoff", t.backoff)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	wait := time.NewTimer(t.backoff)
	defer wait.Stop()
	select {
	case <-wait.C:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	return t.roundTripOnce(retry)
}

func gatewayDown(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable
}

func replay(req *http.Request) (*http.Request, bool) {
	retry := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return retry, true
	}
	if req.GetBody == nil {
		return nil, false
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	retry.Body = body
	return retry, true
}

func (t *Transport) roundTripOnce(req *http.Request) (*http.Response, error) {
	u := &use{}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: u.got}))
	if upgrading(req) {
		resp, err := t.base.RoundTrip(req)
		if err != nil {
			u.release()
		}
		return resp, err
	}
	var resp *http.Response
	var err error
	if t.stall > 0 {
		resp, err = t.watched(req)
	} else {
		resp, err = t.base.RoundTrip(req)
	}
	if err != nil {
		u.failed(err)
		u.release()
		return nil, err
	}
	resp.Body = &usedBody{ReadCloser: resp.Body, u: u}
	return resp, nil
}

func (t *Transport) CloseIdleConnections() { t.base.CloseIdleConnections() }

func (t *Transport) Stats() Stats { return t.pool.stats() }

func upgrading(req *http.Request) bool { return req.Header.Get("Upgrade") != "" }

var shared = sync.OnceValue(func() *Transport { return New(Defaults) })

func Shared() *Transport { return shared() }
