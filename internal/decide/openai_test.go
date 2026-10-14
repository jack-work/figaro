package decide

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apidecide "github.com/jack-work/figaro/api/decide"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
)

// Recorded 2026-10-14 by aria 9ff8caa5 against https://api.openai.com/v1/decisions
// through rou2 on spain, both question types in one body. Verbatim, including
// the usage members jev does not send and the missing "id" jev does.
const liveOpenAI = `{"model":"gpt-6-luna","answers":[{"type":"choice","name":"department","choice":"billing","probabilities":[{"value":"billing","probability":1.0},{"value":"technical","probability":0.0},{"value":"other","probability":0.0}],"confidence":1.0},{"type":"predicate","name":"angry","probability":0.73}],"usage":{"input_tokens":284,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":284}}`

func renamed(live, from, to string) string {
	return strings.Replace(live, `"name":"`+from+`"`, `"name":"`+to+`"`, 1)
}

var (
	predicateReply = renamed(liveOpenAI, "angry", questionKey)
	choiceReply    = renamed(liveOpenAI, "department", questionKey)
)

type openAISeen struct {
	path string
	raw  []byte
	body openAIRequest
}

func openAIReplying(t *testing.T, reply string) (*Engine, *openAISeen) {
	t.Helper()
	got := &openAISeen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.raw, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(got.raw, &got.body)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return at(srv.URL), got
}

func TestALunaModelSpeaksTheOpenAIWire(t *testing.T) {
	e, got := openAIReplying(t, predicateReply)
	out := decide(t, e, Source{Defaults: lunaDefaults, Log: logOf(t, chatter(4)...)}, rpc.DecideRequest{Question: "is the customer upset?"})
	if got.path != "/decisions" {
		t.Fatalf("path = %q", got.path)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(got.raw, &members); err != nil {
		t.Fatal(err)
	}
	for m := range members {
		switch m {
		case "model", "input", "questions":
		default:
			t.Errorf("%q is not a member of the OpenAI request", m)
		}
	}
	if len(got.body.Questions) != 1 {
		t.Fatalf("questions = %+v", got.body.Questions)
	}
	q := got.body.Questions[0]
	if q.Type != "predicate" || q.Name != questionKey || q.Instructions != "is the customer upset?" {
		t.Fatalf("question = %+v", q)
	}
	if out.API != apidecide.APIOpenAI || out.Model != "gpt-6-luna" {
		t.Fatalf("api=%q model=%q", out.API, out.Model)
	}
	// A predicate is a probability, the same quantity jev's noul is, so yes/no
	// converts without loss; what OpenAI does not send is a confidence.
	if out.Rows[0].Option != "yes" || out.Rows[0].Probability != 0.73 || !out.Rows[0].Chosen {
		t.Fatalf("rows = %+v", out.Rows)
	}
	if out.Confidence != nil {
		t.Fatalf("confidence = %v: a predicate carries none, and none may be invented", *out.Confidence)
	}
	if out.InputToks != 284 {
		t.Fatalf("input tokens = %d", out.InputToks)
	}
}

func TestAChoiceSendsValuesAndDescriptions(t *testing.T) {
	board := boardOf(t, map[string]string{
		"skills.printer.filePath":    `"/s/printer.md"`,
		"skills.printer.frontmatter": `"name: printer"`,
		"skills.weather.filePath":    `"/s/weather.md"`,
		"skills.weather.frontmatter": `"name: weather"`,
	})
	e, got := openAIReplying(t, `{"model":"gpt-6-luna","answers":[{"type":"choice","name":"decision","choice":"printer","probabilities":[{"value":"printer","probability":0.9},{"value":"weather","probability":0.1}],"confidence":0.8}]}`)
	out := decide(t, e, Source{Defaults: lunaDefaults, Board: board}, rpc.DecideRequest{Question: "which skill?\n?? @skills"})
	q := got.body.Questions[0]
	if q.Type != "choice" || len(q.Choices) != 2 {
		t.Fatalf("question = %+v", q)
	}
	var fm map[string]string
	if q.Choices[0].Value != "printer" || json.Unmarshal([]byte(q.Choices[0].Description), &fm) != nil || fm["filePath"] != "/s/printer.md" {
		t.Fatalf("a form child must be a value with its JSON as the description: %+v", q.Choices[0])
	}
	if out.Rows[0].Option != "printer" || !out.Rows[0].Chosen || *out.Confidence != 0.8 {
		t.Fatalf("rows = %+v", out.Rows)
	}
}

func TestALiteralOptionIsAValueWithNoDescription(t *testing.T) {
	e, got := openAIReplying(t, choiceReply)
	out := decide(t, e, Source{Defaults: lunaDefaults}, rpc.DecideRequest{Question: "which department?", Choices: []string{"billing", "technical", "other"}})
	for _, c := range got.body.Questions[0].Choices {
		if c.Description != "" {
			t.Errorf("%q carries a description that only repeats it", c.Value)
		}
	}
	if out.Rows[0].Option != "billing" || out.Rows[0].Probability != 1.0 || len(out.Rows) != 3 {
		t.Fatalf("rows = %+v", out.Rows)
	}
}

func TestTheInputIsTextTheModelCanRead(t *testing.T) {
	msgs := append(chatter(3), say(message.RoleOutput, "the handler has no idempotency key"))
	e, got := openAIReplying(t, predicateReply)
	decide(t, e, Source{Defaults: lunaDefaults, Log: logOf(t, msgs...), Context: "a deploy log"}, rpc.DecideRequest{Question: "<4>! is a retry safe?"})
	in := got.body.Input
	for _, want := range []string{"Focus", "the handler has no idempotency key", "Context:\na deploy log", "Conversation, oldest first:", "you: line 00"} {
		if !strings.Contains(in, want) {
			t.Fatalf("%q missing from the input:\n%s", want, in)
		}
	}
	if strings.HasPrefix(strings.TrimSpace(in), "{") {
		t.Fatalf("the input is JSON, not text:\n%s", in)
	}
}

func TestARefusalIsAnError(t *testing.T) {
	e, _ := openAIReplying(t, `{"model":"gpt-6-luna","answers":[{"type":"refusal","name":"decision"}]}`)
	_, err := e.Decide(context.Background(), Source{Defaults: lunaDefaults}, rpc.DecideRequest{Question: "q?"})
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("err = %v", err)
	}
}

func TestLunaGetsItsOwnBudget(t *testing.T) {
	// Measured 2026-10-14 through Experiential Labs: 763,222 input tokens were
	// accepted and ~1.0M refused with "Decision input exceeds the token limit".
	// jev's 32k is jev's; luna must not inherit it.
	huge := strings.Repeat("why does the retry loop charge twice? ", 4000)
	e, _ := openAIReplying(t, predicateReply)
	out := decide(t, e, Source{Defaults: lunaDefaults}, rpc.DecideRequest{Question: huge})
	if out.Window.Budget < 500000 {
		t.Fatalf("luna's budget = %d, still jev-sized", out.Window.Budget)
	}
	jev, _ := engineReplying(t, liveNoul)
	if _, err := jev.Decide(context.Background(), Source{Defaults: jevDefaults}, rpc.DecideRequest{Question: huge}); err == nil {
		t.Fatal("the same question fit jev's 32k; the fixture proves nothing")
	}
}

func TestTheOpenAITokenLimitRejectionIsRetriedSmaller(t *testing.T) {
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sizes = append(sizes, len(body))
		if len(sizes) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Decision input exceeds the token limit.","type":"invalid_request_error","param":null,"code":null}}`)
			return
		}
		io.WriteString(w, predicateReply)
	}))
	defer srv.Close()
	long := make([]message.Message, 0, 300)
	for i := 0; i < 300; i++ {
		long = append(long, say(message.RoleOutput, strings.Repeat("tool output ", 4000)))
	}
	e := at(srv.URL)
	if _, err := e.Decide(context.Background(), Source{Defaults: lunaDefaults, Log: logOf(t, long...)}, rpc.DecideRequest{Question: "q?"}); err != nil {
		t.Fatalf("no retry: %v", err)
	}
	if len(sizes) != 2 || sizes[1] >= sizes[0] {
		t.Fatalf("sizes = %v", sizes)
	}
}

func TestTheAPIComesFromTheRequestThenTheBoardThenTheDefaultOutfitThenTheModel(t *testing.T) {
	board := boardOf(t, map[string]string{APIKey: `"openai"`})
	cases := []struct {
		name  string
		board bool
		req   string
		file  string
		model string
		want  string
	}{
		{"model infers jev", false, "", "", "jev-latest", "/systemone"},
		{"model infers luna", false, "", "", "gpt-6-luna", "/decisions"},
		{"the default outfit beats the model", false, "", "openai", "jev-latest", "/decisions"},
		{"the board beats the default outfit", true, "", "systemone", "jev-latest", "/decisions"},
		{"the request beats the board", true, "systemone", "", "gpt-6-luna", "/systemone"},
	}
	for _, c := range cases {
		var path string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			if r.URL.Path == "/decisions" {
				io.WriteString(w, predicateReply)
				return
			}
			io.WriteString(w, liveNoul)
		}))
		file, model := c.file, c.model
		e := at(srv.URL)
		set := map[string]json.RawMessage{ProviderKey: json.RawMessage(`"test"`), ModelKey: json.RawMessage(`"` + model + `"`)}
		if file != "" {
			set[APIKey] = json.RawMessage(`"` + file + `"`)
		}
		src := Source{Defaults: form.Snapshot{}.Apply(form.Build(form.Snapshot{}, set, nil))}
		if c.board {
			src.Board = board
		}
		if _, err := e.Decide(context.Background(), src, rpc.DecideRequest{Question: "q?", API: c.req}); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		srv.Close()
		if path != c.want {
			t.Errorf("%s: posted to %s, want %s", c.name, path, c.want)
		}
	}
}

func TestAnUnknownAPIIsRefusedByName(t *testing.T) {
	e, _ := engineReplying(t, liveNoul)
	_, err := e.Decide(context.Background(), Source{Defaults: lunaDefaults}, rpc.DecideRequest{Question: "q?", API: "grpc"})
	if err == nil || !strings.Contains(err.Error(), "grpc") {
		t.Fatalf("err = %v", err)
	}
}
