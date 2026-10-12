package auth

import (
	"errors"
	"syscall"
)

// THE AGENT IS MORTAL, AND IT DIES ON A SCHEDULE.
//
// hush's agent exits when its TTL runs out, and that TTL is ABSOLUTE: counted
// from the moment it started, never extended by use. figaro sets it to a day,
// so every agent dies exactly one day after it was born, and every request in
// flight against its socket then fails with "connect: no such file or
// directory" until something starts a new one. The daemon's keep-alive did,
// on a ticker of up to five minutes, which made a daily outage of up to five
// minutes in which every turn of every aria whose credential lives in the
// vault failed (Gluck, 2026-10-12, two boxes). It reaches the agent dying of
// anything else the same way: a crash, a stop in a dev shell sharing the
// surface.
//
// The repair is at the point of use. A vault call that finds no agent brings
// one up and asks once more. Starting the embedded agent is a spawn and an
// unlock from the keyring or the passphrase file, so the turn pays well under
// a second, once a day, instead of failing.

// AgentGone reports whether a hush call failed because no agent is serving
// the socket: the socket file is missing, or nothing is listening on it. Any
// other failure is the agent's answer and is not retried.
func AgentGone(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// WithAgent runs call, and if it failed because the agent is gone, runs
// revive and call once more. A nil revive, or a revive that fails, returns
// the original error: the caller's own message stays the diagnosis, with the
// revival failure joined to it.
func WithAgent[T any](revive func() error, call func() (T, error)) (T, error) {
	out, err := call()
	if err == nil || revive == nil || !AgentGone(err) {
		return out, err
	}
	if rerr := revive(); rerr != nil {
		var zero T
		return zero, errors.Join(err, &ReviveError{Err: rerr})
	}
	return call()
}

// ReviveError is a failure to bring the agent back. It is its own type so a
// message for the user can say "the vault agent would not start" instead of
// blaming whatever the call was trying to do.
type ReviveError struct{ Err error }

func (e *ReviveError) Error() string { return "hush agent would not start: " + e.Err.Error() }
func (e *ReviveError) Unwrap() error { return e.Err }
