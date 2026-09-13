package cli

import (
	"net"
	"sync/atomic"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/mark"
)

var connSeq atomic.Uint64

// markTap counts every byte of one aria connection into the mark sink, so a
// hop's wire cost is measured where it is paid rather than estimated from the
// pages it returned. Composed with the tape tap when both are on.
func markTap(inner transport.Tap) transport.Tap {
	if !mark.Enabled() {
		return inner
	}
	return func(c net.Conn) net.Conn {
		if inner != nil {
			c = inner(c)
		}
		return &markedConn{Conn: c, gen: connSeq.Add(1)}
	}
}

type markedConn struct {
	net.Conn
	gen uint64
}

func (m *markedConn) Read(p []byte) (int, error) {
	n, err := m.Conn.Read(p)
	if n > 0 {
		mark.Mark("wire.rx", "conn", m.gen, "bytes", n)
	}
	return n, err
}

func (m *markedConn) Write(p []byte) (int, error) {
	n, err := m.Conn.Write(p)
	if n > 0 {
		mark.Mark("wire.tx", "conn", m.gen, "bytes", n)
	}
	return n, err
}
