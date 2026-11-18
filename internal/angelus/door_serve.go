package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jack-work/jkrpc"
)

const doorRequestsInFlight = 32

func serveRequests(ctx context.Context, conn *jkrpc.Conn, handlers map[string]jkrpc.HandlerFunc) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	slots := make(chan struct{}, doorRequestsInFlight)
	for {
		msg, err := conn.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("recv: %w", err)
		}
		if !msg.IsRequest() {
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return nil
		}
		go func(msg jkrpc.Message) {
			defer func() { <-slots }()
			answerDoor(ctx, conn, handlers, msg)
		}(msg)
	}
}

func answerDoor(ctx context.Context, conn *jkrpc.Conn, handlers map[string]jkrpc.HandlerFunc, msg jkrpc.Message) {
	handler, ok := handlers[msg.Method]
	if !ok {
		_ = conn.Send(jkrpc.Message{JSONRPC: "2.0", ID: msg.ID,
			Error: &jkrpc.Error{Code: -32601, Message: fmt.Sprintf("method not found: %s", msg.Method)}})
		return
	}
	result, err := handler(ctx, msg.Params)
	if err != nil {
		jerr, typed := err.(*jkrpc.Error)
		if !typed {
			jerr = &jkrpc.Error{Code: -32000, Message: err.Error()}
		}
		_ = conn.Send(jkrpc.Message{JSONRPC: "2.0", ID: msg.ID, Error: jerr})
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		_ = conn.Send(jkrpc.Message{JSONRPC: "2.0", ID: msg.ID,
			Error: &jkrpc.Error{Code: -32603, Message: "failed to marshal result"}})
		return
	}
	_ = conn.Send(jkrpc.Message{JSONRPC: "2.0", ID: msg.ID, Result: raw})
}
