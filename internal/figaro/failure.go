package figaro

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/provider"
)

type FailureCategory string

const (
	FailureRateLimited   FailureCategory = "rate_limited"
	FailureAuth          FailureCategory = "auth"
	FailureProviderError FailureCategory = "provider_error"
	FailureTimeout       FailureCategory = "timeout"
	FailureRefused       FailureCategory = "refused"
	FailureOther         FailureCategory = "other"
)

func failureCategory(fault error) FailureCategory {
	if fault == nil {
		return FailureOther
	}
	var limited *provider.RateLimited
	var late *provider.FirstByteTimeoutError
	var completion *provider.CompletionFault
	switch {
	case errors.As(fault, &limited):
		return FailureRateLimited
	case errors.As(fault, &late), errors.Is(fault, context.DeadlineExceeded):
		return FailureTimeout
	case errors.As(fault, &completion) && (completion.Wire == "refusal" || completion.Wire == "content_filter"):
		return FailureRefused
	}
	if _, ok := auth.SignInNeeded(fault); ok {
		return FailureAuth
	}
	return FailureProviderError
}

func failedReason(reason string) (string, bool) {
	rest, ok := strings.CutPrefix(reason, "error:")
	return strings.TrimSpace(rest), ok
}

func (a *Agent) recordFailure(reason string) {
	if a.form == nil || a.backend == nil {
		return
	}
	snap := a.form.Snapshot()
	var patch form.Patch
	if why, failed := failedReason(reason); failed {
		set := map[string]json.RawMessage{}
		for k, v := range map[string]any{
			form.FailureReasonKey:   why,
			form.FailureCategoryKey: failureCategory(a.turnFault),
			form.FailureTurnKey:     a.turnID,
			form.FailureAtKey:       time.Now().UnixMilli(),
		} {
			set[k], _ = json.Marshal(v)
		}
		patch = form.Build(snap, set, nil)
	} else {
		patch = form.Build(snap, nil, []string{form.FailureReasonKey, form.FailureCategoryKey, form.FailureTurnKey, form.FailureAtKey})
	}
	if patch.IsIdentity() {
		return
	}
	if _, err := a.backend.ApplyFormPrivileged(a.id, patch); err != nil {
		slog.Error("record turn failure", "aria", a.id, "err", err)
		return
	}
	a.form.Apply(patch)
}
