package gateway

// THE FILTER: the one place the tunnel looks at what it carries.
//
// A pure pump cannot enforce a method policy, so when a policy is configured
// the client->daemon direction runs through here instead. The rule that keeps
// this honest: DECODE TO CHECK, FORWARD THE ORIGINAL. The bytes that reach
// the daemon are the bytes the client sent, never a re-encoding, so no field
// this gateway does not know about can be dropped on the way through.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// maxFrame bounds one NDJSON line the filter will buffer. A frame larger
// than this is refused rather than truncated: truncation would change the
// meaning of what we forward, which is the one thing this file promises not
// to do.
const maxFrame = 8 << 20 // 8 MiB

// Decision is a filter's verdict on one method.
type Decision struct {
	Allow  bool
	Reason string
}

// Filter inspects each client->server frame. It is where the two things that
// must happen per-frame live: the method policy, and the attribution rewrite
// that stops a remote caller naming itself any aria.
type Filter struct {
	// Check is consulted once per request frame. Nil allows everything --
	// which is a decision the caller makes explicitly, not a default.
	Check func(method string) Decision
	// Rewrite replaces a frame's params before forwarding. Nil forwards the
	// original bytes verbatim. See rewriteEnvelope for why the payload is
	// still never re-encoded even when this is set.
	Rewrite func(params json.RawMessage) (json.RawMessage, error)
}

// frame is the sliver of JSON-RPC the filter needs. Everything else in the
// message is deliberately not modelled: this struct exists to read a method
// name and an id, not to represent the protocol.
type frame struct {
	Method string           `json:"method"`
	ID     *json.RawMessage `json:"id"`
	Params json.RawMessage  `json:"params"`
}

// Pump copies src to dst, refusing frames the policy denies. A denied
// request is answered on the CLIENT's stream with a JSON-RPC error carrying
// its id, so the caller gets a verdict rather than a hang.
//
// src must be the client side (we write refusals back to it); dst the daemon.
func (f *Filter) Pump(src io.ReadWriter, dst io.Writer) error {
	br := bufio.NewReaderSize(src, 64<<10)
	// A json.Decoder would be tidier, but it buffers past the value it
	// returns, and we must forward the EXACT bytes of each message. Reading
	// discrete NDJSON lines is what makes verbatim forwarding possible.
	for {
		line, err := readLine(br, maxFrame)
		if err != nil {
			return err
		}
		if len(line) == 0 {
			continue
		}

		var fr frame
		if json.Unmarshal(line, &fr) != nil {
			// Unparseable: let the daemon reject it in its own words. We are
			// a policy check, not a validator, and inventing an error here
			// would put this gateway in the business of defining the wire.
			if _, werr := dst.Write(line); werr != nil {
				return werr
			}
			continue
		}

		if fr.Method != "" && f.Check != nil {
			if d := f.Check(fr.Method); !d.Allow {
				if fr.ID != nil {
					if werr := writeRefusal(src, *fr.ID, fr.Method, d.Reason); werr != nil {
						return werr
					}
				}
				continue // never reaches the daemon
			}
		}

		// ATTRIBUTION IS REPLACED, never trusted. Only the params object is
		// rebuilt; the surrounding frame is reassembled from the fields we
		// decoded, and every params key we do not own survives byte-exact
		// as a RawMessage.
		if f.Rewrite != nil && fr.Method != "" {
			rebuilt, err := f.rewriteLine(line, fr)
			if err != nil {
				if fr.ID != nil {
					if werr := writeRefusal(src, *fr.ID, fr.Method, "malformed request envelope"); werr != nil {
						return werr
					}
				}
				continue
			}
			line = rebuilt
		}

		if _, werr := dst.Write(line); werr != nil {
			return werr
		}
	}
}

// rewriteLine rebuilds one frame with rewritten params. It decodes the whole
// message into a raw map so that any top-level field this gateway has never
// heard of -- a jsonrpc extension, a future envelope slot -- is carried
// across untouched rather than dropped by a typed round trip.
func (f *Filter) rewriteLine(line []byte, fr frame) ([]byte, error) {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil, err
	}
	next, err := f.Rewrite(fr.Params)
	if err != nil {
		return nil, err
	}
	msg["params"] = next
	out, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// readLine returns one NDJSON line INCLUDING its terminating newline, so the
// bytes forwarded are byte-for-byte what arrived.
func readLine(br *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > limit {
			return nil, fmt.Errorf("frame exceeds %d bytes", limit)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if len(buf) > 0 && err == io.EOF {
				return buf, nil
			}
			return nil, err
		}
		return buf, nil
	}
}

// refusalCode is the JSON-RPC error code for a method the policy refused.
// It sits in the -32000..-32099 application range and is distinct from the
// daemon's own codes so a client can tell "the gateway said no" from "the
// daemon said no".
const refusalCode = -32040

func writeRefusal(w io.Writer, id json.RawMessage, method, reason string) error {
	if reason == "" {
		reason = "refused by gateway policy"
	}
	msg := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{JSONRPC: "2.0", ID: id}
	msg.Error.Code = refusalCode
	msg.Error.Message = fmt.Sprintf("%s: %s", method, reason)

	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}
