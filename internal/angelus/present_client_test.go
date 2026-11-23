package angelus

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/peer/peertest"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/jkrpc"
)

type reissuingBearer struct {
	is      *peertest.Issuer
	renewed atomic.Bool
}

func (b *reissuingBearer) Resolve() (string, error) {
	if b.renewed.Load() {
		return b.is.Mint("probe", time.Hour), nil
	}
	return b.is.Mint("probe", 2*time.Second), nil
}

func (b *reissuingBearer) Invalidate(string) error {
	b.renewed.Store(true)
	return nil
}

func TestAClientKeepsItsConnectionAlivePastTheTokenItDialedWith(t *testing.T) {
	is := peertest.NewIssuer(t)
	issuers, err := authz.NewIssuers(is.Config("probe"), nil, nil)
	require.NoError(t, err)
	who, err := issuers.Open(context.Background(), is.Mint("probe", time.Hour))
	require.NoError(t, err)
	path := shortSocketPath(t)
	h := &handlers{angelus: &Angelus{Hubs: newHubs(), Policies: seats(t, who.Principal().String())}}
	h.angelus.Handlers = h.door(map[string]jkrpc.HandlerFunc{rpc.MethodHello: h.hello, rpc.MethodEndpointPresent: h.endpointPresent}, authz.AllowAll())
	ln, err := ListenUnix(path, 0o660)
	require.NoError(t, err)
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go h.angelus.ServeWeb(ctx, ln, Web{Admit: authz.Bearers{Issuers: issuers}.AdmitRequest, MaxConnAge: time.Hour})

	bearer := &reissuingBearer{is: is}
	ep := transport.Endpoint{Scheme: "wss", Address: "wss://fig.test/", Bearer: bearer, HTTPClient: &http.Client{Transport: &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}}}
	acli, err := sdk.DialAngelus(ep)
	require.NoError(t, err)
	defer acli.Close()
	dialedAt := time.Now()

	time.Sleep(time.Until(dialedAt.Add(2500 * time.Millisecond)))
	hctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = acli.Hello(hctx)
	require.NoError(t, err, "the connection lapsed with its first token; the client never presented a fresh one")
	require.True(t, bearer.renewed.Load())
}
