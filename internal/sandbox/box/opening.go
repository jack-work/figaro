package box

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/jack-work/jkrpc"
)

const maxOpening = 1 << 20

type Opening struct {
	Read      []byte
	Scope     string
	Workspace string
	id        *int64
}

func ReadOpening(r io.Reader) (Opening, error) {
	var buf []byte
	chunk := make([]byte, 4096)
	for {
		n, err := r.Read(chunk)
		buf = append(buf, chunk[:n]...)
		end := bytes.IndexByte(buf[len(buf)-n:], '\n')
		if end >= 0 {
			end += len(buf) - n
		}
		if end > maxOpening || (end < 0 && len(buf) > maxOpening) {
			return Opening{}, fmt.Errorf("box: the opening request is longer than %d bytes", maxOpening)
		}
		if end >= 0 {
			return parseOpening(buf, buf[:end])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return Opening{}, errors.New("box: the connection closed before box.open")
			}
			return Opening{}, err
		}
	}
}

func parseOpening(read, line []byte) (Opening, error) {
	var msg jkrpc.Message
	if err := json.Unmarshal(line, &msg); err != nil {
		return Opening{}, fmt.Errorf("box: the first request is not JSON-RPC: %w", err)
	}
	if !msg.IsRequest() || msg.Method != mOpen {
		return Opening{}, fmt.Errorf("box: the first request is %q, not %s", msg.Method, mOpen)
	}
	var req openReq
	if err := json.Unmarshal(msg.Params, &req); err != nil {
		return Opening{}, fmt.Errorf("box: %s: %w", mOpen, err)
	}
	return Opening{Read: read, Scope: req.Scope, Workspace: req.Workspace, id: msg.ID}, nil
}

func (o Opening) Refuse(w io.Writer, refusal error) error {
	return json.NewEncoder(w).Encode(jkrpc.Message{JSONRPC: "2.0", ID: o.id, Error: toWire(refusal).(*jkrpc.Error)})
}

type replayed struct {
	net.Conn
	r io.Reader
}

func (c replayed) Read(p []byte) (int, error) { return c.r.Read(p) }

func (s *Server) ServeConn(ctx context.Context, nc net.Conn, read []byte) {
	if len(read) > 0 {
		nc = replayed{Conn: nc, r: io.MultiReader(bytes.NewReader(read), nc)}
	}
	s.serveConn(ctx, nc)
	s.mu.Lock()
	defer s.mu.Unlock()
	for scope := range s.scopes {
		s.host.KillScope(scope)
	}
}
