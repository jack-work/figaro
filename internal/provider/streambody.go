package provider

import (
	"bytes"
	"io"
	"net/http"
)

// BodyFunc writes one complete request body to w.
type BodyFunc func(io.Writer) error

// RequestBody is a request body and its framing. Both framings run the SAME
// BodyFunc, so the bytes on the wire do not depend on which one a setting
// chose.
type RequestBody struct {
	fn     BodyFunc
	buffer []byte
}

// NewRequestBody produces the body fn writes. When streamed, the bytes exist
// only as they are written; otherwise they are materialized here, once, and
// reused by every retry.
func NewRequestBody(fn BodyFunc, streamed bool) (*RequestBody, error) {
	if streamed {
		return &RequestBody{fn: fn}, nil
	}
	var buf bytes.Buffer
	if err := fn(&buf); err != nil {
		return nil, err
	}
	return &RequestBody{buffer: buf.Bytes()}, nil
}

// Attach installs the body on req. A streamed body sets GetBody, because
// without it a replay -- an HTTP/2 GOAWAY, a 307 -- is a hard failure; a
// writer fault closes the pipe WITH its error, so the transport aborts the
// request rather than ending the body early.
func (b *RequestBody) Attach(req *http.Request) {
	if b.fn == nil {
		req.Body = io.NopCloser(bytes.NewReader(b.buffer))
		req.ContentLength = int64(len(b.buffer))
		buf := b.buffer
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(buf)), nil
		}
		return
	}
	body := func() io.ReadCloser {
		pr, pw := io.Pipe()
		go func() { _ = pw.CloseWithError(b.fn(pw)) }()
		return pr
	}
	req.Body = body()
	req.GetBody = func() (io.ReadCloser, error) { return body(), nil }
	req.ContentLength = -1
}

// Len is the body's size in bytes, or -1 when it is streamed and unknown.
func (b *RequestBody) Len() int {
	if b.fn != nil {
		return -1
	}
	return len(b.buffer)
}
