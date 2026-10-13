package provider

import (
	"bytes"
	"io"
	"net/http"
)

type BodyFunc func(io.Writer) error

// RequestBody runs the same BodyFunc under either framing.
type RequestBody struct {
	fn     BodyFunc
	buffer []byte
}

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

// Attach sets GetBody on a streamed body so a replay (GOAWAY, 307) works; a
// writer fault closes the pipe with its error so the transport aborts.
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

// Len is -1 when streamed.
func (b *RequestBody) Len() int {
	if b.fn != nil {
		return -1
	}
	return len(b.buffer)
}
