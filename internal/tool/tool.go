package tool

import "context"

type Tool interface {
	Name() string
	Description() string
	Parameters() interface{}
	Execute(ctx context.Context, args map[string]interface{}) (string, error)
}
