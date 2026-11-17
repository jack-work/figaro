package figaro

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
)

const lengthCutBudget = 1

type lengthCut struct {
	calls  []string
	tokens int
}

func lengthCutOf(streamed *asm, landed message.Message) lengthCut {
	if landed.StopReason != message.StopLength || streamed == nil {
		return lengthCut{}
	}
	kept := map[string]bool{}
	for _, c := range assistantToolInvokes(landed) {
		kept[c.ToolCallID] = true
	}
	var cut lengthCut
	for _, c := range streamed.msg.Content {
		if c.Type == message.ContentToolInvoke && !kept[c.ToolCallID] {
			cut.calls = append(cut.calls, c.ToolName)
		}
	}
	if landed.Usage != nil {
		cut.tokens = landed.Usage.OutputTokens
	}
	return cut
}

func (a *Agent) lengthCutText(cut lengthCut) string {
	limit := "the output limit (max_tokens)"
	n := cut.tokens
	if n <= 0 {
		n = a.formInt("system.max_tokens")
	}
	if n > 0 {
		limit = fmt.Sprintf("the output limit (max_tokens=%d)", n)
	}
	return fmt.Sprintf("your reply hit %s while writing a call to %s; the call was cut off and did not run. "+
		"Write the content in smaller pieces.", limit, strings.Join(cut.calls, ", "))
}

func (a *Agent) tellLengthCut(cut lengthCut) error {
	notice := "continue: " + a.lengthCutText(cut)
	a.emitCommit()
	if _, err := a.appendUserPrompt(event{
		typ:      eventUserPrompt,
		text:     notice,
		segments: []promptSegment{{sender: resumeSender, text: notice}},
	}, true); err != nil {
		return err
	}
	a.startAssistantUnit()
	a.emitDelta(a.composeTurn(nil))
	return nil
}

func (a *Agent) continueAfterLengthCut(cut lengthCut) (done bool) {
	if a.lengthCuts >= lengthCutBudget {
		a.endTurn("error: " + a.lengthCutText(cut))
		return true
	}
	a.lengthCuts++
	slog.Warn("reply hit max_tokens inside a tool call, continuing the turn", "aria", a.id, "tools", cut.calls)
	if err := a.tellLengthCut(cut); err != nil {
		a.endTurn(fmt.Sprintf("error: %s; append the notice: %v", a.lengthCutText(cut), err))
		return true
	}
	a.publishRuntime(rpc.RuntimeThinking, "reply hit max_tokens inside a tool call; asked the model to continue in smaller pieces")
	return false
}
