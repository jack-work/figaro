package cli

import (
	"errors"
	"time"

	"github.com/zalando/go-keyring"
)

// Every keyring call in figaro goes through this file, and every one of
// them is bounded.
//
// The reason is a WSL box where gnome-keyring is running but no Secret
// Service collection exists and none can be created: CreateCollection
// raises a prompt and there is no prompter to answer it. keyring.Get on
// a locked collection does not fail there. It BLOCKS, forever, inside
// dbus, with no output. `figaro vault status`, the one command whose
// whole job is to explain why the vault is stuck, was itself the thing
// that hung.
//
// The goroutine that made the call is deliberately left running: it is
// blocked in dbus and cannot be cancelled, so we abandon it and let
// process exit collect it. Leaking one goroutine beats hanging the CLI.

const keyringTimeout = 5 * time.Second

// errKeyringTimeout is what a hung Secret Service looks like from here.
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

// keyringReachable reports whether this host has a keyring that answers.
// "Not found" is an answer and counts as reachable: the entry is simply
// not written yet. A timeout, a dbus error, or a missing provider all
// count as no keyring, which is what the create prompt needs to know
// before it promises the user he will never be asked again.
func keyringReachable(service, account string) bool {
	if service == "" || account == "" {
		return false
	}
	_, err := keyringGet(service, account)
	return err == nil || errors.Is(err, keyring.ErrNotFound)
}
