package decide

import (
	"errors"

	"github.com/jack-work/figaro/api/rpc"
)

var ErrUpstream = errors.New("the decision route failed")

type upstreamError struct{ err error }

func (e upstreamError) Error() string   { return e.err.Error() }
func (e upstreamError) Unwrap() []error { return []error{e.err, ErrUpstream} }

func fromUpstream(err error) error {
	if err == nil {
		return nil
	}
	return upstreamError{err}
}

func (src Source) Route() (string, error) {
	s, err := src.settings(rpc.DecideRequest{})
	if err != nil {
		return "", err
	}
	route := s.provider + "/" + s.model
	if s.baseURL != "" {
		route += " at " + s.baseURL
	}
	return route, nil
}
