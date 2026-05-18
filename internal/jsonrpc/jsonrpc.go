// Package jsonrpc implements minimal JSON-RPC 2.0 over NDJSON.
// One reader goroutine, one writer goroutine, ordered delivery.
package jsonrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Message is a JSON-RPC 2.0 message.
type Message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *int64           `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
}

// Error is a JSON-RPC 2.0 error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// IsRequest returns true if this is a request (has ID and Method).
func (m *Message) IsRequest() bool {
	return m.ID != nil && m.Method != ""
}

// IsResponse returns true if this is a response (has ID, no Method).
func (m *Message) IsResponse() bool {
	return m.ID != nil && m.Method == ""
}

// IsNotification returns true if this is a notification (Method, no ID).
func (m *Message) IsNotification() bool {
	return m.ID == nil && m.Method != ""
}

// Conn wraps an io.ReadWriteCloser with JSON-RPC 2.0 encoding.
// Send is mutex-protected; Recv is single-goroutine.
type Conn struct {
	rwc io.ReadWriteCloser
	dec *json.Decoder
	mu  sync.Mutex // protects enc
	enc *json.Encoder
}

// NewConn wraps a stream as a JSON-RPC connection.
func NewConn(rwc io.ReadWriteCloser) *Conn {
	return &Conn{
		rwc: rwc,
		dec: json.NewDecoder(rwc),
		enc: json.NewEncoder(rwc),
	}
}

// Send writes a message. Goroutine-safe.
func (c *Conn) Send(msg Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(msg)
}

// Recv reads the next message. Blocks. Single-goroutine.
func (c *Conn) Recv() (Message, error) {
	var msg Message
	if err := c.dec.Decode(&msg); err != nil {
		return msg, err
	}
	return msg, nil
}

// Close closes the underlying stream.
func (c *Conn) Close() error {
	return c.rwc.Close()
}

// HandlerFunc handles a JSON-RPC request.
type HandlerFunc func(ctx context.Context, params json.RawMessage) (interface{}, error)

// Server dispatches requests from a Conn to handlers.
type Server struct {
	conn     *Conn
	handlers map[string]HandlerFunc
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewServer creates a server for one connection.
func NewServer(conn *Conn, handlers map[string]HandlerFunc) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		conn:     conn,
		handlers: handlers,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
}

// Serve processes messages until close or cancel. Blocks.
func (s *Server) Serve(ctx context.Context) error {
	defer close(s.done)

	for {
		msg, err := s.conn.Recv()
		if err != nil {
			if err == io.EOF || s.ctx.Err() != nil || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("recv: %w", err)
		}

		if msg.IsRequest() {
			s.handleRequest(ctx, msg)
		}

	}
}

func (s *Server) handleRequest(ctx context.Context, msg Message) {
	handler, ok := s.handlers[msg.Method]
	if !ok {
		s.conn.Send(Message{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Error:   &Error{Code: -32601, Message: fmt.Sprintf("method not found: %s", msg.Method)},
		})
		return
	}

	result, err := handler(ctx, msg.Params)
	if err != nil {
		// If the handler returned a typed *Error, pass it through
		// verbatim (preserves Code + Data); otherwise wrap in -32000.
		var jerr *Error
		if typed, ok := err.(*Error); ok {
			jerr = typed
		} else {
			jerr = &Error{Code: -32000, Message: err.Error()}
		}
		s.conn.Send(Message{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Error:   jerr,
		})
		return
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		s.conn.Send(Message{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Error:   &Error{Code: -32603, Message: "failed to marshal result"},
		})
		return
	}

	s.conn.Send(Message{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  resultJSON,
	})
}

// Notify sends a notification. Goroutine-safe.
func (s *Server) Notify(method string, params interface{}) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return s.conn.Send(Message{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsJSON,
	})
}

// Stop cancels the server.
func (s *Server) Stop() {
	s.cancel()
	s.conn.Close()
}

// Wait blocks until the server exits.
func (s *Server) Wait() {
	<-s.done
}

// NotifyFunc handles server notifications (called in wire order).
type NotifyFunc func(method string, params json.RawMessage)

// Client sends requests and receives responses over a Conn.
type Client struct {
	conn     *Conn
	onNotify NotifyFunc
	nextID   atomic.Int64

	mu       sync.Mutex
	pending  map[int64]chan Message // waiting for response by ID
	closed   bool
	done     chan struct{}
}

// NewClient creates a client. onNotify may be nil.
func NewClient(conn *Conn, onNotify NotifyFunc) *Client {
	c := &Client{
		conn:     conn,
		onNotify: onNotify,
		pending:  make(map[int64]chan Message),
		done:     make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// readLoop routes responses to callers and delivers notifications.
func (c *Client) readLoop() {
	defer close(c.done)
	for {
		msg, err := c.conn.Recv()
		if err != nil {
			// Connection closed; wake pending calls.
			c.mu.Lock()
			c.closed = true
			for _, ch := range c.pending {
				close(ch)
			}
			c.pending = nil
			c.mu.Unlock()
			return
		}

		if msg.IsResponse() {
			c.mu.Lock()
			ch, ok := c.pending[*msg.ID]
			if ok {
				delete(c.pending, *msg.ID)
			}
			c.mu.Unlock()
			if ok {
				ch <- msg
			}
		} else if msg.IsNotification() {
			if c.onNotify != nil {
				c.onNotify(msg.Method, msg.Params)
			}
		}
	}
}

// Call sends a request and blocks for the response.
func (c *Client) Call(ctx context.Context, method string, params, result interface{}) error {
	id := c.nextID.Add(1)

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}


	ch := make(chan Message, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("connection closed")
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.conn.Send(Message{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  paramsJSON,
	}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("send: %w", err)
	}


	select {
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("connection closed while waiting for response")
		}
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && resp.Result != nil {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

// Close closes the connection.
func (c *Client) Close() error {
	err := c.conn.Close()
	<-c.done
	return err
}

// Done returns a channel closed when the connection dies.
func (c *Client) Done() <-chan struct{} {
	return c.done
}
