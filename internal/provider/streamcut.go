package provider

import (
	"errors"
	"fmt"
)

type StreamCut struct {
	Provider string
	Awaited  string
	Events   int
	Err      error
}

func (c *StreamCut) Error() string {
	who := c.Provider
	if who == "" {
		who = "the provider"
	}
	awaited := c.Awaited
	if awaited == "" {
		awaited = "its last event"
	}
	if c.Err == nil {
		return fmt.Sprintf("%s: the upstream closed the stream after %d events, before %s", who, c.Events, awaited)
	}
	return fmt.Sprintf("%s: the upstream cut the stream after %d events, before %s (%v)", who, c.Events, awaited, c.Err)
}

func (c *StreamCut) Unwrap() error { return c.Err }

func AsStreamCut(err error) (*StreamCut, bool) {
	var cut *StreamCut
	if errors.As(err, &cut) {
		return cut, true
	}
	return nil, false
}

func NameStreamCut(err error, provider string) error {
	if cut, ok := AsStreamCut(err); ok && cut.Provider == "" {
		cut.Provider = provider
	}
	return err
}
