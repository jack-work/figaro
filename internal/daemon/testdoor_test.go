package daemon

import (
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// testDoor holds one bound port for a node's whole test life, across every
// restart. Each start serves a tenancy of it. With no tenancy serving, a
// connection is closed at once, as a stopped node's refusal would be, so a
// peer dialing a node that has not started fails fast instead of waiting
// for it; a stopping door closes its tenancy and the port stays bound, so
// no other test can take it between a halt and the next start.
type testDoor struct {
	ln  net.Listener
	mu  sync.Mutex
	cur *doorTenancy
}

func openTestDoor(t *testing.T) *testDoor {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	d := &testDoor{ln: ln}
	go d.accept()
	t.Cleanup(func() { ln.Close() })
	return d
}

func (d *testDoor) accept() {
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

func (d *testDoor) addr() string { return d.ln.Addr().String() }

func (d *testDoor) tenancy() *doorTenancy {
	l := &doorTenancy{door: d, conns: make(chan net.Conn), done: make(chan struct{})}
	d.mu.Lock()
	d.cur = l
	d.mu.Unlock()
	return l
}

type doorTenancy struct {
	door  *testDoor
	conns chan net.Conn
	once  sync.Once
	done  chan struct{}
}

func (l *doorTenancy) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *doorTenancy) Close() error {
	l.once.Do(func() {
		l.door.mu.Lock()
		if l.door.cur == l {
			l.door.cur = nil
		}
		l.door.mu.Unlock()
		close(l.done)
	})
	return nil
}

func (l *doorTenancy) closed() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

func (l *doorTenancy) Addr() net.Addr { return l.door.ln.Addr() }
