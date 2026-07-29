package figaro

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
)

// TestAssembleToolResultsImages covers what a tool_result tic carries once
// tool imagery is no longer discarded: attribution, ordering, the error
// path, and the per-message budget that keeps one screenshot from
// overflowing a WAL segment and destroying the turn.
func TestAssembleToolResultsImages(t *testing.T) {
	big := strings.Repeat("A", toolImageBudget+1)
	half := strings.Repeat("B", (toolImageBudget/2)+1)

	tests := []struct {
		name       string
		calls      []message.Content
		outcomes   map[string]toolOutcome
		wantText   map[string]string
		wantImages []message.Content
		wantNote   string
	}{
		{
			name:  "text only is untouched",
			calls: []message.Content{toolCall("tc_1", "bash")},
			outcomes: map[string]toolOutcome{
				"tc_1": {content: []message.Content{message.TextContent("hello")}},
			},
			wantText: map[string]string{"tc_1": "hello"},
		},
		{
			name:  "image rides alongside its prose",
			calls: []message.Content{toolCall("tc_1", "read")},
			outcomes: map[string]toolOutcome{
				"tc_1": {content: []message.Content{
					message.TextContent("[Image: a.png]"),
					message.ImageContent("image/png", "AAAA"),
				}},
			},
			wantText: map[string]string{"tc_1": "[Image: a.png]"},
			wantImages: []message.Content{
				message.ToolImageContent("tc_1", "read", "image/png", "AAAA"),
			},
		},
		{
			name:  "several images in one result all survive, in order",
			calls: []message.Content{toolCall("tc_1", "read")},
			outcomes: map[string]toolOutcome{
				"tc_1": {content: []message.Content{
					message.ImageContent("image/png", "AAAA"),
					message.TextContent("between"),
					message.ImageContent("image/jpeg", "BBBB"),
				}},
			},
			wantText: map[string]string{"tc_1": "between"},
			wantImages: []message.Content{
				message.ToolImageContent("tc_1", "read", "image/png", "AAAA"),
				message.ToolImageContent("tc_1", "read", "image/jpeg", "BBBB"),
			},
		},
		{
			name: "images from several tools stay bound to their calls",
			calls: []message.Content{
				toolCall("tc_1", "read"),
				toolCall("tc_2", "shot"),
			},
			outcomes: map[string]toolOutcome{
				"tc_1": {content: []message.Content{message.ImageContent("image/png", "AAAA")}},
				"tc_2": {content: []message.Content{message.ImageContent("image/gif", "CCCC")}},
			},
			wantText: map[string]string{"tc_1": "", "tc_2": ""},
			wantImages: []message.Content{
				message.ToolImageContent("tc_1", "read", "image/png", "AAAA"),
				message.ToolImageContent("tc_2", "shot", "image/gif", "CCCC"),
			},
		},
		{
			name:  "an errored tool still delivers its picture",
			calls: []message.Content{toolCall("tc_1", "shot")},
			outcomes: map[string]toolOutcome{
				"tc_1": {isErr: true, content: []message.Content{
					message.TextContent("capture failed midway"),
					message.ImageContent("image/png", "AAAA"),
				}},
			},
			wantText: map[string]string{"tc_1": "capture failed midway"},
			wantImages: []message.Content{
				message.ToolImageContent("tc_1", "shot", "image/png", "AAAA"),
			},
		},
		{
			name:  "an oversized image is dropped and announced",
			calls: []message.Content{toolCall("tc_1", "shot")},
			outcomes: map[string]toolOutcome{
				"tc_1": {content: []message.Content{
					message.TextContent("[Image: huge.png]"),
					message.ImageContent("image/png", big),
				}},
			},
			wantNote: "image omitted",
		},
		{
			name:  "the budget is spent across a whole message, not per image",
			calls: []message.Content{toolCall("tc_1", "shot")},
			outcomes: map[string]toolOutcome{
				"tc_1": {content: []message.Content{
					message.ImageContent("image/png", half),
					message.ImageContent("image/png", half),
				}},
			},
			wantImages: []message.Content{
				message.ToolImageContent("tc_1", "shot", "image/png", half),
			},
			wantNote: "image omitted",
		},
		{
			name:  "an empty image block is not carried",
			calls: []message.Content{toolCall("tc_1", "read")},
			outcomes: map[string]toolOutcome{
				"tc_1": {content: []message.Content{message.ImageContent("image/png", "")}},
			},
			wantText: map[string]string{"tc_1": ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expect := make(map[string]bool, len(tt.calls))
			for _, c := range tt.calls {
				expect[c.ToolCallID] = true
			}

			a := &Agent{}
			tic := a.assembleToolResults(tt.calls, expect, tt.outcomes)

			require.Equal(t, message.RoleInput, tic.Role)

			var images []message.Content
			results := map[string]message.Content{}
			for i, c := range tic.Content {
				switch c.Type {
				case message.ContentToolResult:
					require.Less(t, i, len(tt.calls), "tool results must precede images")
					results[c.ToolCallID] = c
				case message.ContentImage:
					images = append(images, c)
				default:
					t.Fatalf("unexpected block type %q", c.Type)
				}
			}
			require.Len(t, results, len(tt.calls))

			for id, want := range tt.wantText {
				assert.Equal(t, want, results[id].Text)
			}
			assert.Equal(t, tt.wantImages, images)

			if tt.wantNote != "" {
				var joined string
				for _, r := range results {
					joined += r.Text
				}
				assert.Contains(t, joined, tt.wantNote)
			}
		})
	}
}

// TestAssembleToolResultsBudgetKeepsTicUnderSegment is the reason the budget
// exists at all: the tic is one figwal record, and a record larger than a WAL
// segment fails the append and takes the turn with it. store.segment_size may
// legally be set as low as 1MiB.
func TestAssembleToolResultsBudgetKeepsTicUnderSegment(t *testing.T) {
	const minSegment = 1 << 20
	assert.Less(t, toolImageBudget, minSegment,
		"the image budget must leave room for the result text sharing the record")

	calls := []message.Content{toolCall("tc_1", "shot"), toolCall("tc_2", "shot")}
	outcomes := map[string]toolOutcome{
		"tc_1": {content: []message.Content{message.ImageContent("image/png", strings.Repeat("A", toolImageBudget))}},
		"tc_2": {content: []message.Content{message.ImageContent("image/png", strings.Repeat("B", toolImageBudget))}},
	}
	expect := map[string]bool{"tc_1": true, "tc_2": true}

	tic := (&Agent{}).assembleToolResults(calls, expect, outcomes)

	total := 0
	for _, c := range tic.Content {
		total += len(c.Data)
	}
	assert.LessOrEqual(t, total, toolImageBudget,
		"two tools must share one budget, not get one each")
}
