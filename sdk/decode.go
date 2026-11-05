package sdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
)

type DecodeError struct {
	Method string
	Err    error
}

func (e *DecodeError) Error() string {
	builds := "the daemon is likely another build than this client"
	if f := peerBuilds.Load(); f != nil {
		if d := (*f)(); d != "" {
			builds = d
		}
	}
	return fmt.Sprintf("the daemon's answer to %s does not decode (%v):\n%s\nrun `figaro stop` and retry: the next command starts a daemon from this binary",
		e.Method, e.Err, builds)
}

func (e *DecodeError) Unwrap() error { return e.Err }

var peerBuilds atomic.Pointer[func() string]

func DescribePeerBuilds(f func() string) { peerBuilds.Store(&f) }

func decoded(method string, err error) error {
	var typ *json.UnmarshalTypeError
	var syn *json.SyntaxError
	if errors.As(err, &typ) || errors.As(err, &syn) {
		return &DecodeError{Method: method, Err: err}
	}
	return err
}
