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

// Filter checks each request's method before forwarding it.
type Filter struct {
	// Check is consulted once per request frame. Notifications and responses
	// from the client (there are none in practice) pass untouched.
	Check func(method string) Decision
}

// frame is the sliver of JSON-RPC the filter needs. Everything else in the
// message is deliberately not modelled: this struct exists to read a method
// name and an id, not to represent the protocol.
type frame struct {
	Method string           `json:"method"`
	ID     *json.RawMessage `json:"id"`
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

		if _, werr := dst.Write(line); werr != nil {
			return werr
		}
	}
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
