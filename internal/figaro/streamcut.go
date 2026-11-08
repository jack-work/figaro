package figaro

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/provider"
)

var streamCutBackoff = []time.Duration{time.Second, 3 * time.Second}

func streamCutNotice(cut *provider.StreamCut) string {
	return fmt.Sprintf("continue: %s, so figaro resent the request. "+
		"Your reply above stops where the stream did, and a tool call that was still being written was dropped. "+
		"Carry on from there.", cut.Error())
}

func (a *Agent) resumeAfterStreamCut(turnCtx context.Context, cut *provider.StreamCut, spec *specDispatcher, specDone <-chan struct{}, toolBuf []toolEvent) (resumed, done bool) {
	a.waitWithForks(specDone)
	if a.isInterrupted() || turnCtx.Err() != nil || spec.started() || len(toolBuf) > 0 {
		return false, false
	}
	if a.streamCuts >= len(streamCutBackoff) {
		return false, false
	}
	wait := streamCutBackoff[a.streamCuts]
	a.streamCuts++
	slog.Warn("stream cut before message_stop, resuming the turn", "aria", a.id, "attempt", a.streamCuts, "err", cut)
	if _, err := a.repairTurnTail(); err != nil {
		a.reconcileAriaServer()
		a.finishTurn(fmt.Sprintf("error: %v; repair after the cut: %v", cut, err))
		return true, true
	}
	a.emitDelta(a.composeTurn(nil))
	notice := streamCutNotice(cut)
	a.emitCommit()
	if _, err := a.appendUserPrompt(event{
		typ:      eventUserPrompt,
		text:     notice,
		segments: []promptSegment{{sender: resumeSender, text: notice}},
	}, true); err != nil {
		a.endTurn(fmt.Sprintf("error: %v; append the resume notice: %v", cut, err))
		return true, true
	}
	a.startAssistantUnit()
	a.emitDelta(a.composeTurn(nil))
	a.publishRuntime(rpc.RuntimeThinking, fmt.Sprintf("stream cut by %s; resent the request (%d/%d)", cut.Provider, a.streamCuts, len(streamCutBackoff)))
	select {
	case <-turnCtx.Done():
	case <-time.After(wait):
	}
	return true, false
}
