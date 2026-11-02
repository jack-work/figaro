package upstream

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"
)

type Config struct {
	SendPingTimeout time.Duration
	PingTimeout     time.Duration
	Stall           time.Duration
	Dial            func(ctx context.Context, network, addr string) (net.Conn, error)
	TLS             *tls.Config
}

var Defaults = Config{
	SendPingTimeout: 15 * time.Second,
	PingTimeout:     10 * time.Second,
	Stall:           90 * time.Second,
}

type Transport struct {
	base  *http.Transport
	stall time.Duration
}

func New(cfg Config) *Transport {
	dial := cfg.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	return &Transport{
		stall: cfg.Stall,
		base: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dial,
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
	if t.stall <= 0 || upgrading(req) {
		return t.base.RoundTrip(req)
	}
	return t.watched(req)
}

func (t *Transport) CloseIdleConnections() { t.base.CloseIdleConnections() }

func upgrading(req *http.Request) bool { return req.Header.Get("Upgrade") != "" }

var shared = sync.OnceValue(func() *Transport { return New(Defaults) })

func Shared() *Transport { return shared() }
