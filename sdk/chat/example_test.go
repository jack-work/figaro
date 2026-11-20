package chat_test

import (
	"context"
	"fmt"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk/chat"
)

func Example() {
	ctx := context.Background()
	c := chat.New(transport.UnixEndpoint("/run/figaro/angelus.sock"))
	defer c.Close()

	thread, err := c.Open(ctx, "ac3a069e553a68e760050460733cada3")
	if err != nil {
		return
	}
	events, err := c.Prompt(ctx, thread, "What is on my calendar today?")
	if err != nil || events == nil {
		return
	}
	var last chat.Cursor
	for e := range events {
		last = e.Cursor
		switch e.Kind {
		case chat.Delta:
			fmt.Print(e.Text)
		case chat.ToolStart:
			fmt.Printf("\n[%s %s]\n", e.Tool.Name, e.Tool.Args)
		case chat.End:
			fmt.Println()
		case chat.Failed:
			fmt.Println("\nfailed:", e.Text)
		}
	}

	rest, err := c.Resume(ctx, thread, last)
	if err == nil {
		for range rest {
		}
	}

	h, err := c.History(ctx, thread, 0, 20)
	if err == nil {
		for _, t := range h.Turns {
			fmt.Printf("> %s\n%s\n", t.Prompt, t.Reply)
		}
	}
}
