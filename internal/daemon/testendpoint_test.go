package daemon

import (
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// testEndpoint holds one bound port for a node's whole test life, across every
// restart. Each start serves a tenancy of it. With no tenancy serving, a
// connection is closed at once, as a stopped node's refusal would be, so a
// peer dialing a node that has not started fails fast instead of waiting
// for it; a stopping endpoint closes its tenancy and the port stays bound, so
// no other test can take it between a halt and the next start.
type testEndpoint struct {
	ln  net.Listener
	mu  sync.Mutex
	cur *endpointTenancy
}

func openTestEndpoint(t *testing.T) *testEndpoint {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	d := &testEndpoint{ln: ln}
	go d.accept()
	t.Cleanup(func() { ln.Close() })
	return d
}

func (d *testEndpoint) accept() {
	for {
		c, err := d.ln.Accept()
		if err != nil {
			return
		}
		d.mu.Lock()
		cur := d.cur
		d.mu.Unlock()
		if cur == nil {
			c.Close()
			continue
		}
		select {
		case cur.conns <- c:
		case <-cur.done:
			c.Close()
		}
	}
}

func (d *testEndpoint) addr() string { return d.ln.Addr().String() }

func (d *testEndpoint) tenancy() *endpointTenancy {
	l := &endpointTenancy{endpoint: d, conns: make(chan net.Conn), done: make(chan struct{})}
	d.mu.Lock()
	d.cur = l
	d.mu.Unlock()
	return l
}

type endpointTenancy struct {
	endpoint *testEndpoint
	conns    chan net.Conn
	once     sync.Once
	done     chan struct{}
}

func (l *endpointTenancy) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *endpointTenancy) Close() error {
	l.once.Do(func() {
		l.endpoint.mu.Lock()
		if l.endpoint.cur == l {
			l.endpoint.cur = nil
		}
		l.endpoint.mu.Unlock()
		close(l.done)
	})
	return nil
}

func (l *endpointTenancy) closed() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

func (l *endpointTenancy) Addr() net.Addr { return l.endpoint.ln.Addr() }
