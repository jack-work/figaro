package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

var errEmptyStdin = errors.New("--stdin: stdin held no prompt")

type ariaFormReader interface {
	Form(ctx context.Context) (*rpc.FormResponse, error)
}

func ariaIsLog(ctx context.Context, a quaer) bool {
	r, ok := a.(ariaFormReader)
	if !ok {
		return true
	}
	resp, err := r.Form(ctx)
	if err != nil {
		return true
	}
	return form.IsLog(resp.Snapshot)
}

func blankStdin(d dressing, prompt string) bool {
	return d.exact && strings.TrimSpace(prompt) == "" && len(d.images) == 0
}

func settleExact(ctx context.Context, a quaer, req rpc.QuaRequest) (rpc.QuaRequest, error) {
	if !req.Exact {
		return req, nil
	}
	if !daemonLacks(rpc.CapLogExact) {
		return req, nil
	}
	if !ariaIsLog(ctx, a) {
		req.Exact = false
		req.Text = strings.TrimRight(req.Text, "\n")
		if strings.TrimSpace(req.Text) == "" && len(req.Images) == 0 {
			return req, errEmptyStdin
		}
		return req, nil
	}
	return req, lacksForUse(rpc.CapLogExact, "an exact entry on an aria log")
}
