package tool

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/message"
)

const SayMaxChars = 4096

type SayTool struct {
	Say func(ctx context.Context, text string) error
}

func (t *SayTool) Name() string { return "say" }

func (t *SayTool) Description() string {
	return "Send a message to the group chat you answer. The text is posted as it is, up to 4096 characters."
}

func (t *SayTool) Parameters() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"text": map[string]interface{}{"type": "string", "description": "The message to post"},
		},
		"required": []string{"text"},
	}
}

func (t *SayTool) Execute(ctx context.Context, args map[string]interface{}, _ OnOutput) ([]message.Content, error) {
	text, _ := args["text"].(string)
	switch n := utf8.RuneCountInString(text); {
	case n == 0:
		return nil, errors.New("chat: no text to post")
	case n > SayMaxChars:
		return nil, errors.New("chat: the text is over 4096 characters; send it in parts")
	}
	if err := t.Say(ctx, text); err != nil {
		return nil, err
	}
	return []message.Content{message.TextContent("posted")}, nil
}
