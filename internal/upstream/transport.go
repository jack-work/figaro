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
	Dial            func(ctx context.Context, network, addr string) (net.Conn, error)
	TLS             *tls.Config
}

var Defaults = Config{
	SendPingTimeout: 15 * time.Second,
	PingTimeout:     10 * time.Second,
}

func New(cfg Config) http.RoundTripper {
	dial := cfg.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	return &http.Transport{
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
	}
}

var shared = sync.OnceValue(func() http.RoundTripper { return New(Defaults) })

func Shared() http.RoundTripper { return shared() }
