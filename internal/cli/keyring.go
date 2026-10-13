package cli

import (
	"errors"
	"time"

	"github.com/zalando/go-keyring"
)

// Every keyring call is bounded: on a locked Secret Service collection with no
// prompter (WSL), keyring.Get blocks forever inside dbus. The blocked goroutine
// cannot be cancelled and is abandoned to process exit.
const keyringTimeout = 5 * time.Second

var errKeyringTimeout = errors.New("keyring did not answer")

type keyringResult struct {
	value string
	err   error
}

func boundedKeyring(call func() (string, error)) (string, error) {
	return boundedKeyringWithin(keyringTimeout, call)
}

func boundedKeyringWithin(limit time.Duration, call func() (string, error)) (string, error) {
	done := make(chan keyringResult, 1)
	go func() {
		v, err := call()
		done <- keyringResult{v, err}
	}()
	select {
	case r := <-done:
		return r.value, r.err
	case <-time.After(limit):
		return "", errKeyringTimeout
	}
}

func keyringGet(service, account string) (string, error) {
	return boundedKeyring(func() (string, error) { return keyring.Get(service, account) })
}

func keyringSet(service, account, value string) error {
	_, err := boundedKeyring(func() (string, error) { return "", keyring.Set(service, account, value) })
	return err
}

func keyringDelete(service, account string) error {
	_, err := boundedKeyring(func() (string, error) { return "", keyring.Delete(service, account) })
	return err
}

// "Not found" is an answer and counts as reachable; a timeout, a dbus error, or
// a missing provider count as no keyring.
func keyringReachable(service, account string) bool {
	if service == "" || account == "" {
		return false
	}
	_, err := keyringGet(service, account)
	return err == nil || errors.Is(err, keyring.ErrNotFound)
}
