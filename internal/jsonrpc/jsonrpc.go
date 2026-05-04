// Package jsonrpc implements a minimal JSON-RPC 2.0 client and server
// over any io.ReadWriteCloser, framed as newline-delimited JSON.
//
// Ordering is guaranteed by the transport (TCP, unix socket, websocket).
// One reader goroutine, one writer goroutine. No goroutine pools,
// no concurrent dispatch.
//
// This replaces creachadair/jrpc2 which dispatches OnNotify from
// concurrent goroutines, causing reordering.
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
// It can be a request (ID + Method), response (ID + Result/Error),
// or notification (Method, no ID).
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
	Code    int    `json:"code"`
	Message string `json:"message"`
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

// --- Conn: ordered JSON-RPC read/write over a stream ---

// Conn wraps an io.ReadWriteCloser with JSON-RPC 2.0 encoding.
// Send is safe for concurrent use (mutex-protected).
// Recv must be called from a single goroutine.
type Conn struct {
	rwc io.ReadWriteCloser
	dec *json.Decoder
	mu  sync.Mutex // protects enc
	enc *json.Encoder
}

// NewConn wraps a stream (net.Conn, pipe, etc.) as a JSON-RPC connection.
func NewConn(rwc io.ReadWriteCloser) *Conn {
	return &Conn{
		rwc: rwc,
		dec: json.NewDecoder(rwc),
		enc: json.NewEncoder(rwc),
	}
}

// Send writes a message to the connection. Safe for concurrent use.
func (c *Conn) Send(msg Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(msg)
}

// Recv reads the next message from the connection. Blocks until a
// message is available. Must be called from a single goroutine.
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

// --- HandlerFunc ---

// HandlerFunc handles a JSON-RPC request. params is the raw JSON params.
// Returns the result to be sent in the response, or an error.
type HandlerFunc func(ctx context.Context, params json.RawMessage) (interface{}, error)

// --- Server: serves one connection ---

// Server reads requests from a Conn, dispatches to handlers, and sends
// responses. Notifications to the client are sent via Notify(), which
// goes through the same Conn.Send (mutex-protected, ordered).
type Server struct {
	conn     *Conn
	handlers map[string]HandlerFunc
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewServer creates a server for a single connection.
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

// Serve reads and processes messages until the connection closes or
// ctx is cancelled. Blocks.
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
		// Notifications from client and responses are ignored.
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
		s.conn.Send(Message{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Error:   &Error{Code: -32000, Message: err.Error()},
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

// Notify sends a notification to the client. Thread-safe, ordered
// with respect to other Send calls (responses, other notifications).
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

// --- Client: sends requests, receives responses + notifications ---

// NotifyFunc is called for each notification from the server.
// Called synchronously on the reader goroutine — in wire order.
type NotifyFunc func(method string, params json.RawMessage)

// Client sends requests and receives responses over a Conn.
// Server notifications are delivered to OnNotify synchronously
// on the reader goroutine — ordered, no goroutine pool.
type Client struct {
	conn     *Conn
	onNotify NotifyFunc
	nextID   atomic.Int64

	mu       sync.Mutex
	pending  map[int64]chan Message // waiting for response by ID
	closed   bool
	done     chan struct{}
}

// NewClient creates a client. onNotify is called for each server
// notification, synchronously on the reader goroutine (ordered).
// May be nil to discard notifications.
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

// readLoop reads messages from the connection. Responses are routed
// to pending Call waiters. Notifications are delivered to onNotify.
// Runs on a single goroutine — all delivery is ordered.
func (c *Client) readLoop() {
	defer close(c.done)
	for {
		msg, err := c.conn.Recv()
		if err != nil {
			// Connection closed or error — wake all pending calls.
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

// Call sends a request and blocks until the response arrives.
func (c *Client) Call(ctx context.Context, method string, params, result interface{}) error {
	id := c.nextID.Add(1)

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	// Register before sending so we don't miss the response.
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

	// Wait for response or context cancellation.
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

// Close closes the connection and waits for the read loop to exit.
func (c *Client) Close() error {
	err := c.conn.Close()
	<-c.done
	return err
}

// Done returns a channel that is closed when the client's read loop
// exits — i.e. when the underlying connection is closed by either
// side, or hits an unrecoverable read error. Useful for detecting an
// agent that died mid-turn.
func (c *Client) Done() <-chan struct{} {
	return c.done
}
