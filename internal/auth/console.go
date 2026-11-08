package auth

import "context"

type Console interface {
	Note(text string)
	Link(url string)
	Code(code string)
	Done(text string)
	Ask(ctx context.Context, prompt string) (string, error)
	AskSecret(ctx context.Context, prompt string) ([]byte, error)
}
