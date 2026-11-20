package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

const maxLogEntryBytes = 15 << 20

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
	lacks := daemonLacks(rpc.CapLogExact)
	valid := utf8.ValidString(req.Text)
	if !lacks && valid && len(req.Text) <= maxLogEntryBytes {
		return req, nil
	}
	if !ariaIsLog(ctx, a) {
		if !lacks {
			return req, nil
		}
		req.Exact = false
		req.Text = strings.TrimRight(req.Text, "\n")
		if strings.TrimSpace(req.Text) == "" && len(req.Images) == 0 {
			return req, errEmptyStdin
		}
		return req, nil
	}
	if lacks {
		return req, lacksForUse(rpc.CapLogExact, "an exact entry on an aria log")
	}
	if !valid {
		return req, errors.New("--stdin: an aria log entry must be valid UTF-8, and stdin is not")
	}
	return req, fmt.Errorf("--stdin: an aria log entry holds at most %d bytes (15 MiB); stdin held %d", maxLogEntryBytes, len(req.Text))
}
