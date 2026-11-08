package auth

import (
	"context"
	"errors"
)

type quiet struct{}

func (quiet) Note(string)      {}
func (quiet) Link(string)      {}
func (quiet) Code(string)      {}
func (quiet) Done(string)      {}
func (quiet) Open(string) bool { return false }
func (quiet) Ask(context.Context, string) (string, error) {
	return "", errors.New("the test console answers nothing")
}
func (quiet) AskSecret(context.Context, string) ([]byte, error) {
	return nil, errors.New("the test console answers nothing")
}
