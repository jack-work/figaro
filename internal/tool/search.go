package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/message"
)

const (
	SearchMaxQuery    = 400
	SearchMaxResults  = 10
	SearchResultsSent = 5
	searchFieldCap    = 600
	searchAnswerCap   = 8 << 10
)

type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type SearchTool struct {
	Search func(ctx context.Context, query string, count int) ([]SearchResult, error)
}

func (t *SearchTool) Name() string { return "search" }

func (t *SearchTool) Description() string {
	return "Search the web. Returns up to count results, each a title, a URL and a snippet."
}

func (t *SearchTool) Parameters() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{"type": "string", "description": "What to search for, up to 400 characters"},
			"count": map[string]interface{}{"type": "integer", "description": "How many results, 1 to 10; 5 when omitted", "minimum": 1, "maximum": SearchMaxResults},
		},
		"required": []string{"query"},
	}
}

func (t *SearchTool) Execute(ctx context.Context, args map[string]interface{}, _ OnOutput) ([]message.Content, error) {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	switch n := utf8.RuneCountInString(query); {
	case n == 0:
		return nil, errors.New("search: no query")
	case n > SearchMaxQuery:
		return nil, fmt.Errorf("search: the query is over %d characters", SearchMaxQuery)
	}
	count := SearchResultsSent
	if raw, ok := args["count"]; ok {
		f, isNumber := raw.(float64)
		if !isNumber || f != float64(int(f)) || f < 1 || f > SearchMaxResults {
			return nil, fmt.Errorf("search: count is a whole number from 1 to %d", SearchMaxResults)
		}
		count = int(f)
	}
	results, err := t.Search(ctx, query, count)
	if err != nil {
		return nil, err
	}
	return []message.Content{message.TextContent(searchAnswer(results, count))}, nil
}

func searchAnswer(results []SearchResult, count int) string {
	if len(results) == 0 {
		return "no results"
	}
	var b strings.Builder
	for i, r := range results[:min(len(results), count)] {
		entry := fmt.Sprintf("%d. %s\n%s\n%s\n\n", i+1, clipped(r.Title), clipped(r.URL), clipped(r.Snippet))
		if b.Len()+len(entry) > searchAnswerCap {
			break
		}
		b.WriteString(entry)
	}
	return strings.TrimRight(b.String(), "\n")
}

func clipped(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= searchFieldCap {
		return s
	}
	return string([]rune(s)[:searchFieldCap]) + "…"
}
