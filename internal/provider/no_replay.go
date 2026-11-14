package provider

import "errors"

type noReplayError struct{ cause error }

func (e *noReplayError) Error() string { return e.cause.Error() }
func (e *noReplayError) Unwrap() error { return e.cause }

func WithoutReplay(err error) error {
	if err == nil {
		return nil
	}
	return &noReplayError{cause: err}
}

func ReplayForbidden(err error) bool {
	var blocked *noReplayError
	return errors.As(err, &blocked)
}
