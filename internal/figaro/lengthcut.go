package figaro

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
)

const lengthCutBudget = 1

func lengthCutCalls(streamed *asm, landed message.Message) []string {
	if landed.StopReason != message.StopLength || streamed == nil {
		return nil
	}
	kept := map[string]bool{}
	for _, c := range assistantToolInvokes(landed) {
		kept[c.ToolCallID] = true
	}
	var names []string
	for _, c := range streamed.msg.Content {
		if c.Type == message.ContentToolInvoke && !kept[c.ToolCallID] {
			names = append(names, c.ToolName)
		}
	}
	return names
}

func (a *Agent) lengthCutText(names []string) string {
	limit := "max_tokens"
	if n := a.formInt("system.max_tokens"); n > 0 {
		limit = fmt.Sprintf("max_tokens=%d", n)
	}
	return fmt.Sprintf("your reply hit the output limit (%s) while writing a call to %s; the call was cut off and did not run. "+
		"Write the content in smaller pieces, or raise system.max_tokens.", limit, strings.Join(names, ", "))
}

func (a *Agent) tellLengthCut(names []string) error {
	notice := "continue: " + a.lengthCutText(names)
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

func (a *Agent) continueAfterLengthCut(names []string) (done bool) {
	if a.lengthCuts >= lengthCutBudget {
		a.endTurn("error: " + a.lengthCutText(names))
		return true
	}
	a.lengthCuts++
	slog.Warn("reply hit max_tokens inside a tool call, continuing the turn", "aria", a.id, "tools", names)
	if err := a.tellLengthCut(names); err != nil {
		a.endTurn(fmt.Sprintf("error: %s; append the notice: %v", a.lengthCutText(names), err))
		return true
	}
	a.publishRuntime(rpc.RuntimeThinking, "reply hit max_tokens inside a tool call; asked the model to continue in smaller pieces")
	return false
}
