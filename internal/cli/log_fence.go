package cli

import (
	"bytes"
	"io"

	"github.com/jack-work/figaro/internal/provider"
)

type fenceWriter struct {
	w   io.Writer
	buf []byte
}

func (f *fenceWriter) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	cut := bytes.LastIndexByte(f.buf, '\n')
	if cut < 0 {
		return len(p), nil
	}
	if _, err := io.WriteString(f.w, provider.NeutraliseReservedTags(string(f.buf[:cut+1]))); err != nil {
		return 0, err
	}
	f.buf = append(f.buf[:0], f.buf[cut+1:]...)
	return len(p), nil
}

func (f *fenceWriter) Flush() error {
	if len(f.buf) == 0 {
		return nil
	}
	_, err := io.WriteString(f.w, provider.NeutraliseReservedTags(string(f.buf)))
	f.buf = f.buf[:0]
	return err
}

func fenceStdout() func() {
	prev := stdout
	fw := &fenceWriter{w: prev}
	stdout = fw
	return func() {
		_ = fw.Flush()
		stdout = prev
	}
}
