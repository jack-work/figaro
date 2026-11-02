package upstream

import (
	"context"
	"crypto/tls"
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
	base  *http.Transport
	stall time.Duration
	pool  *pool
}

func New(cfg Config) *Transport {
	dial := cfg.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	p := newPool(cfg)
	return &Transport{
		stall: cfg.Stall,
		pool:  p,
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
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
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
