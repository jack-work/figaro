package decide

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// What api.experientiallabs.ai/v1/systemone answered on 2026-10-12, verbatim.
// The members beyond TypeSafe's documented shape (id, cost, is_byok,
// provider) are why this is a recording: a decoder that only met its author's
// idea of the wire is not tested.
const liveChoice = `{"id":"decision_9165d94ead5cd539b5c57fa072abdc0e","model":"jev-latest",` +
	`"answers":{"decision":{"type":"choice","choice":"idempotency-key","confidence":0.98,` +
	`"probabilities":{"idempotency-key":0.99,"queue":0.0,"nothing":0.01}}},` +
	`"usage":{"input_tokens":388,"output_tokens":46,"cost":0.0,"is_byok":false},"provider":"typesafe"}`

const liveNoul = `{"id":"decision_7589a096666134e798f85a2b23c6a913","model":"jev-latest",` +
	`"answers":{"decision":{"type":"noul","noul":0.97}},` +
	`"usage":{"input_tokens":291,"output_tokens":20,"cost":0.0,"is_byok":false},"provider":"typesafe"}`

type seen struct {
	raw  []byte
	body request
}

func (s *seen) state(t *testing.T) state {
	t.Helper()
	var st state
	if err := json.Unmarshal(s.body.State, &st); err != nil {
		t.Fatalf("state is not an object: %s", s.body.State)
	}
	return st
}

func engineReplying(t *testing.T, reply string) (*Engine, *seen) {
	t.Helper()
	got := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/systemone" {
			t.Errorf("path = %s", r.URL.Path)
		}
		got.raw, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(got.raw, &got.body)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return at(srv.URL), got
}

func logOf(t *testing.T, msgs ...message.Message) store.Log[message.Message] {
	t.Helper()
	log := store.NewMemLog[message.Message]()
	for _, m := range msgs {
		if _, err := log.Append(store.Entry[message.Message]{Payload: m}); err != nil {
			t.Fatal(err)
		}
	}
	return log
}

func say(role message.Role, text string) message.Message {
	return message.Message{Role: role, Content: []message.Content{message.TextContent(text)}}
}

func chatter(n int) []message.Message {
	out := make([]message.Message, 0, n)
	for i := 0; i < n; i++ {
		role := message.RoleInput
		if i%2 == 1 {
			role = message.RoleOutput
		}
		out = append(out, say(role, fmt.Sprintf("line %02d", i)))
	}
	return out
}

func boardOf(t *testing.T, kv map[string]string) form.Snapshot {
	t.Helper()
	set := map[string]json.RawMessage{}
	for k, v := range kv {
		set[k] = json.RawMessage(v)
	}
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, set, nil))
}

func decide(t *testing.T, e *Engine, src Source, req rpc.DecideRequest) rpc.DecideResponse {
	t.Helper()
	out, err := e.Decide(context.Background(), src, req)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	return out
}

func TestTheBodyIsClosed(t *testing.T) {
	// The decisions endpoint refuses a body with any member it does not know.
	// Measured 2026-10-12: "Send only model, state, and bounded typed questions".
	e, got := engineReplying(t, liveChoice)
	decide(t, e, Source{Defaults: jevDefaults, Log: logOf(t, chatter(4)...)}, rpc.DecideRequest{
		Question: "which fix?", Choices: []string{"idempotency-key", "queue", "nothing"},
	})
	var members map[string]json.RawMessage
	if err := json.Unmarshal(got.raw, &members); err != nil {
		t.Fatal(err)
	}
	for m := range members {
		switch m {
		case "model", "state", "questions":
		default:
			t.Fatalf("%q would be refused by the endpoint", m)
		}
	}
}

func TestChoicesFromTheFlagAndTheSigilAreOneList(t *testing.T) {
	e, got := engineReplying(t, liveChoice)
	out := decide(t, e, Source{Defaults: jevDefaults}, rpc.DecideRequest{
		Question: "which fix?\n?? idempotency-key queue",
		Choices:  []string{"nothing", "queue"},
	})
	q := got.body.Questions[questionKey]
	if q.Type != "choice" || len(q.Criteria) != 3 {
		t.Fatalf("question = %+v", q)
	}
	if q.Instructions != "which fix?" {
		t.Fatalf("instructions = %q: the option line must not reach the model as prose", q.Instructions)
	}
	if out.Kind != rpc.DecideChoice || out.Rows[0].Option != "idempotency-key" || !out.Rows[0].Chosen {
		t.Fatalf("rows = %+v", out.Rows)
	}
}

func TestYesNoBecomesANoul(t *testing.T) {
	for _, req := range []rpc.DecideRequest{
		{Question: "is the step size the learning rate?"},
		{Question: "is the step size the learning rate?", Choices: []string{"yes", "no"}},
		{Question: "is the step size the learning rate?\n?? yes no"},
		{Question: "is the step size the learning rate?\n??"},
	} {
		e, got := engineReplying(t, liveNoul)
		out := decide(t, e, Source{Defaults: jevDefaults}, req)
		if q := got.body.Questions[questionKey]; q.Type != "noul" || q.Instructions != "is the step size the learning rate?" {
			t.Fatalf("%+v: question = %+v", req, q)
		}
		if out.Kind != rpc.DecideYesNo || out.Rows[0].Option != "yes" || out.Rows[0].Probability != 0.97 {
			t.Fatalf("%+v: rows = %+v", req, out.Rows)
		}
	}
}

func TestOneOptionIsRefused(t *testing.T) {
	e, _ := engineReplying(t, liveChoice)
	if _, err := e.Decide(context.Background(), Source{Defaults: jevDefaults}, rpc.DecideRequest{Question: "q", Choices: []string{"only"}}); err == nil {
		t.Fatal("a one-option choice was sent")
	}
}

func TestAnEmptyQuestionIsRefused(t *testing.T) {
	e, _ := engineReplying(t, liveNoul)
	if _, err := e.Decide(context.Background(), Source{Defaults: jevDefaults}, rpc.DecideRequest{Question: "  "}); err == nil {
		t.Fatal("an empty yes/no was sent")
	}
}

func TestANonLeafRefListsItsChildrenAsJSON(t *testing.T) {
	// Gluck: "just the frontmatter and the path as a json object. not the
	// full text." One level, no closure.
	board := boardOf(t, map[string]string{
		"skills.printer.filePath":    `"/s/printer.md"`,
		"skills.printer.frontmatter": `"name: printer\ndescription: Send documents to the laser printer."`,
		"skills.weather.filePath":    `"/s/weather.md"`,
		"skills.weather.frontmatter": `"name: weather\ndescription: Short-horizon forecasts."`,
		"mantra":                     `"unrelated"`,
	})
	e, got := engineReplying(t, `{"model":"jev-latest","answers":{"decision":{"type":"choice","choice":"printer","confidence":0.9,"probabilities":{"printer":0.9,"weather":0.1}}}}`)
	out := decide(t, e, Source{Defaults: jevDefaults, Board: board}, rpc.DecideRequest{Question: "which skill?\n?? @skills"})

	q := got.body.Questions[questionKey]
	if len(q.Criteria) != 2 {
		t.Fatalf("criteria = %#v", q.Criteria)
	}
	var printer map[string]string
	if err := json.Unmarshal([]byte(q.Criteria["printer"]), &printer); err != nil {
		t.Fatalf("a child's criterion is not its JSON object: %q", q.Criteria["printer"])
	}
	if printer["filePath"] != "/s/printer.md" || !strings.Contains(printer["frontmatter"], "laser printer") {
		t.Fatalf("printer = %#v", printer)
	}
	// Only the referenced state is sent: nothing else from the form.
	if strings.Contains(string(got.raw), "unrelated") {
		t.Fatalf("an unreferenced form key reached the wire: %s", got.raw)
	}
	if len(out.Window.FormRefs) != 1 || out.Window.FormRefs[0] != "skills" {
		t.Fatalf("refs = %#v", out.Window.FormRefs)
	}
}

func TestALeafRefIsItsStringValue(t *testing.T) {
	board := boardOf(t, map[string]string{
		"plan.a": `"rewrite the retry loop"`,
		"plan.b": `"add an idempotency key"`,
	})
	e, got := engineReplying(t, `{"model":"jev-latest","answers":{"decision":{"type":"choice","choice":"plan.b","probabilities":{"plan.a":0.2,"plan.b":0.8}}}}`)
	decide(t, e, Source{Defaults: jevDefaults, Board: board}, rpc.DecideRequest{Question: "which plan?", Choices: []string{"@plan.a", "@plan.b"}})
	q := got.body.Questions[questionKey]
	if q.Criteria["plan.a"] != "rewrite the retry loop" || q.Criteria["plan.b"] != "add an idempotency key" {
		t.Fatalf("criteria = %#v", q.Criteria)
	}
}

func TestAMissingRefIsNamed(t *testing.T) {
	e, _ := engineReplying(t, liveChoice)
	_, err := e.Decide(context.Background(), Source{Defaults: jevDefaults, Board: form.Snapshot{}}, rpc.DecideRequest{Question: "q", Choices: []string{"@skills"}})
	if err == nil || !strings.Contains(err.Error(), "@skills") {
		t.Fatalf("err = %v", err)
	}
}

func TestTheTailIncludesTheNewestMessage(t *testing.T) {
	// decide writes nothing to the log, so the last entry is conversation, not
	// the question. The turn-provider version of this skipped it.
	e, got := engineReplying(t, liveNoul)
	decide(t, e, Source{Defaults: jevDefaults, Log: logOf(t, chatter(5)...)}, rpc.DecideRequest{Question: "q?"})
	conv := strings.Join(got.state(t).Conversation, "\n")
	if !strings.Contains(conv, "line 04") || !strings.Contains(conv, "line 00") {
		t.Fatalf("conversation = %s", conv)
	}
}

func TestTheConversationTakesWhatTheQuestionLeaves(t *testing.T) {
	e, got := engineReplying(t, liveNoul)
	out := decide(t, e, Source{Defaults: jevDefaults, Log: logOf(t, chatter(40)...)}, rpc.DecideRequest{Question: "q?", Budget: 40})
	st := got.state(t)
	conv := strings.Join(st.Conversation, "\n")
	if !strings.Contains(conv, "line 39") || strings.Contains(conv, "line 00") {
		t.Fatalf("a 40-token window is not a tail:\n%s", conv)
	}
	if out.Window.Messages != len(st.Conversation) || out.Window.Budget != 40 {
		t.Fatalf("window = %+v", out.Window)
	}
}

func TestTheQuestionIsNeverCutToMakeRoom(t *testing.T) {
	// The budget order is the question first. A question that cannot fit is
	// refused locally with the reason; it is never clipped and never sent
	// for the endpoint to refuse with "provider rejected the request".
	e, got := engineReplying(t, liveNoul)
	huge := strings.Repeat("the handler writes a ledger row first. ", 4000)
	_, err := e.Decide(context.Background(), Source{Defaults: jevDefaults, Log: logOf(t, chatter(4)...)}, rpc.DecideRequest{Question: huge})
	if err == nil || !strings.Contains(err.Error(), "question") {
		t.Fatalf("err = %v", err)
	}
	if got.raw != nil {
		t.Fatal("an oversized question was sent")
	}
}

func TestTheWindowShrinksAsTheQuestionGrows(t *testing.T) {
	long := strings.Repeat("x", 40000)
	msgs := make([]message.Message, 0, 40)
	for i := 0; i < 40; i++ {
		msgs = append(msgs, say(message.RoleOutput, long))
	}
	e, _ := engineReplying(t, liveNoul)
	small := decide(t, e, Source{Defaults: jevDefaults, Log: logOf(t, msgs...)}, rpc.DecideRequest{Question: "q?"})
	e, _ = engineReplying(t, liveNoul)
	big := decide(t, e, Source{Defaults: jevDefaults, Log: logOf(t, msgs...)}, rpc.DecideRequest{Question: strings.Repeat("why ", 8000)})
	if big.Window.Budget >= small.Window.Budget {
		t.Fatalf("budget did not shrink: %d vs %d", big.Window.Budget, small.Window.Budget)
	}
	if total := big.Window.Tokens + big.Window.QuestionToks; total > StateLimit {
		t.Fatalf("state + question = %d, over %d", total, StateLimit)
	}
}

func TestAQuotedRegionCentresTheWindow(t *testing.T) {
	msgs := chatter(41)
	msgs[20] = say(message.RoleOutput, "the step size is the learning rate times the gradient")
	e, got := engineReplying(t, liveNoul)
	r := quote.Range{Start: quote.Coord{LT: 21}}
	out := decide(t, e, Source{Defaults: jevDefaults, Log: logOf(t, msgs...)}, rpc.DecideRequest{
		Question: quote.Format(r) + " does the step grow with the slope?",
		Budget:   100,
	})
	st := got.state(t)
	if !strings.Contains(st.Focus, "learning rate times the gradient") {
		t.Fatalf("focus = %q", st.Focus)
	}
	conv := strings.Join(st.Conversation, "\n")
	if !strings.Contains(conv, "line 17") || !strings.Contains(conv, "line 23") {
		t.Fatalf("the window does not straddle lt 21:\n%s", conv)
	}
	if strings.Contains(conv, "line 40") || strings.Contains(conv, "line 00") {
		t.Fatalf("the window drifted to an end:\n%s", conv)
	}
	if got.body.Questions[questionKey].Instructions != "does the step grow with the slope?" {
		t.Fatalf("the coordinate leaked into the question: %q", got.body.Questions[questionKey].Instructions)
	}
	if out.Window.Focus != "<21>!" {
		t.Fatalf("focus coordinate = %q", out.Window.Focus)
	}
}

func TestAQuotedRangeIsTheSelectedRunes(t *testing.T) {
	msgs := []message.Message{say(message.RoleOutput, "alpha beta gamma delta")}
	e, got := engineReplying(t, liveNoul)
	r := quote.Range{Start: quote.Coord{LT: 1, Block: 0, Offset: 6}, End: quote.Coord{LT: 1, Block: 0, Offset: 16}, Block: true, Offsets: true}
	decide(t, e, Source{Defaults: jevDefaults, Log: logOf(t, msgs...)}, rpc.DecideRequest{Question: quote.Format(r) + " greek?"})
	if f := got.state(t).Focus; f != "beta gamma" {
		t.Fatalf("focus = %q", f)
	}
}

func TestAQuoteWithNoAriaIsRefused(t *testing.T) {
	e, _ := engineReplying(t, liveNoul)
	_, err := e.Decide(context.Background(), Source{Defaults: jevDefaults}, rpc.DecideRequest{Question: "<3>! is this right?"})
	if err == nil || !strings.Contains(err.Error(), "no aria") {
		t.Fatalf("err = %v", err)
	}
}

func TestStatelessContextIsSent(t *testing.T) {
	e, got := engineReplying(t, liveNoul)
	decide(t, e, Source{Defaults: jevDefaults, Context: "Gradient descent updates w by -lr * grad."}, rpc.DecideRequest{Question: "does the step scale with the gradient?"})
	if c := got.state(t).Context; !strings.Contains(c, "-lr * grad") {
		t.Fatalf("context = %q", c)
	}
}

func TestNoContextAtAllStillSendsAState(t *testing.T) {
	// The endpoint requires a state; with nothing to send, the question itself
	// is the state rather than an empty object it may refuse.
	e, got := engineReplying(t, liveNoul)
	decide(t, e, Source{Defaults: jevDefaults}, rpc.DecideRequest{Question: "is the sky blue?"})
	var s string
	if err := json.Unmarshal(got.body.State, &s); err != nil || s != "is the sky blue?" {
		t.Fatalf("state = %s", got.body.State)
	}
}

func TestTheEndpointsOwnRefusalIsKept(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"Send only model, state, and bounded typed questions"}}`)
	}))
	defer srv.Close()
	e := at(srv.URL)
	_, err := e.Decide(context.Background(), Source{Defaults: jevDefaults}, rpc.DecideRequest{Question: "q?"})
	if err == nil || !strings.Contains(err.Error(), "bounded typed questions") {
		t.Fatalf("err = %v", err)
	}
}

func TestTiesKeepDeclarationOrder(t *testing.T) {
	e, _ := engineReplying(t, `{"model":"jev-latest","answers":{"decision":{"type":"choice","choice":"a","probabilities":{"a":0.5,"c":0.25,"b":0.25}}}}`)
	out := decide(t, e, Source{Defaults: jevDefaults}, rpc.DecideRequest{Question: "q", Choices: []string{"a", "b", "c"}})
	if out.Rows[1].Option != "b" || out.Rows[2].Option != "c" {
		t.Fatalf("rows = %+v", out.Rows)
	}
}

func TestAHighlightAtTheStartSpillsItsBudgetForward(t *testing.T) {
	// A highlight on the first message has no left side; that half of the
	// budget belongs to the right rather than going unspent.
	e, got := engineReplying(t, liveNoul)
	decide(t, e, Source{Defaults: jevDefaults, Log: logOf(t, chatter(30)...)}, rpc.DecideRequest{Question: "<1>! q?", Budget: 100})
	conv := strings.Join(got.state(t).Conversation, "\n")
	for _, want := range []string{"line 00", "line 03", "line 06"} {
		if !strings.Contains(conv, want) {
			t.Fatalf("%s missing; the unusable half was not spilled:\n%s", want, conv)
		}
	}
}

// Measured 2026-10-13 on Gluck's own pytorch-lore aria (01ecce11): a window
// budgeted at chars/4 billed 1.07x to 1.36x the estimate, the tool-heavy tail
// worst, and past the limit the endpoint refuses the whole request with this
// sentence, which names neither the size nor the limit.
const rejected = `{"error":{"message":"provider rejected the request; verify the request fields against the model alias capabilities","type":"invalid_request_error","param":null,"code":"invalid_request"}}`

func TestARejectedWindowIsRetriedSmaller(t *testing.T) {
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sizes = append(sizes, len(body))
		if len(sizes) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, rejected)
			return
		}
		io.WriteString(w, liveNoul)
	}))
	defer srv.Close()
	long := make([]message.Message, 0, 200)
	for i := 0; i < 200; i++ {
		long = append(long, say(message.RoleOutput, fmt.Sprintf("%03d %s", i, strings.Repeat("tool output ", 200))))
	}
	e := at(srv.URL)
	out, err := e.Decide(context.Background(), Source{Defaults: jevDefaults, Log: logOf(t, long...)}, rpc.DecideRequest{Question: "q?"})
	if err != nil {
		t.Fatalf("the retry did not happen or did not succeed: %v", err)
	}
	if len(sizes) != 2 || sizes[1] >= sizes[0] {
		t.Fatalf("sizes = %v: the retry must send a smaller state", sizes)
	}
	if out.Rows[0].Option != "yes" {
		t.Fatalf("rows = %+v", out.Rows)
	}
}

func TestARejectionThatIsNotAboutSizeIsNotRetried(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()
	e := at(srv.URL)
	if _, err := e.Decide(context.Background(), Source{Defaults: jevDefaults, Log: logOf(t, chatter(40)...)}, rpc.DecideRequest{Question: "q?"}); err == nil {
		t.Fatal("a 401 was swallowed")
	}
	if calls != 1 {
		t.Fatalf("calls = %d: only a size rejection earns a retry", calls)
	}
}

func TestTheEstimateIsConservativeForToolOutput(t *testing.T) {
	// 1.36x chars/4 was the worst ratio measured on real tool output; the
	// estimate has to sit above it or the first attempt is wasted.
	s := strings.Repeat(`{"path":"internal/cli/x.go","line":42}`+"\n", 200)
	if got, floor := tokensOf(s), len(s)*136/400; got < floor {
		t.Fatalf("tokensOf = %d, under the measured %d", got, floor)
	}
}

// The provider names where to post and whose credential to present; the
// engine asks for it by name on every call, so an outfit change takes effect
// without a restart. Microsoft's decision-1 on Foundry takes an Entra bearer
// only, which is why the credential must be the provider's own.
func TestTheProviderSuppliesTheHostAndTheCredential(t *testing.T) {
	var bearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer = r.Header.Get("Authorization")
		io.WriteString(w, liveNoul)
	}))
	defer srv.Close()
	var asked []string
	e := New(func(provider string) (Target, error) {
		asked = append(asked, provider)
		return Target{BaseURL: srv.URL, Auth: fixedToken("entra-access-token")}, nil
	})
	decide(t, e, Source{Defaults: defaultsOf(t, "azure", "decision-1")}, rpc.DecideRequest{Question: "is it urgent?"})
	if len(asked) != 1 || asked[0] != "azure" {
		t.Fatalf("targets asked for %v", asked)
	}
	if bearer != "Bearer entra-access-token" {
		t.Fatalf("Authorization = %q", bearer)
	}
}

func TestAProviderThatCannotBeResolvedIsTheError(t *testing.T) {
	e := New(func(string) (Target, error) { return Target{}, errors.New("no such provider (have: anthropic, rou2)") })
	_, err := e.Decide(context.Background(), Source{Defaults: defaultsOf(t, "nope", "jev-latest")}, rpc.DecideRequest{Question: "q?"})
	if err == nil || !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "rou2") {
		t.Fatalf("err = %v", err)
	}
}

func TestNoProviderOrModelSaysWhereToSetThem(t *testing.T) {
	e, got := engineReplying(t, liveNoul)
	for _, src := range []Source{{}, {Defaults: defaultsOf(t, "", "jev-latest")}, {Defaults: defaultsOf(t, "rou2", "")}} {
		_, err := e.Decide(context.Background(), src, rpc.DecideRequest{Question: "q?"})
		if err == nil || !strings.Contains(err.Error(), "[system.decide]") {
			t.Fatalf("err = %v", err)
		}
	}
	if got.raw != nil {
		t.Fatal("a call with no provider or no model went out")
	}
}

// Azure serves its chat API and decision-1 at different paths of one
// resource, so the provider's base_url is right for turns and wrong for
// decide. system.decide.base_url says where decisions go; the credential is
// still the provider's.
func TestABaseURLOverrideKeepsTheProvidersCredential(t *testing.T) {
	var path, bearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, bearer = r.URL.Path, r.Header.Get("Authorization")
		io.WriteString(w, liveNoul)
	}))
	defer srv.Close()
	e := New(func(string) (Target, error) {
		return Target{BaseURL: "http://127.0.0.1:1/openai/v1", Auth: fixedToken("entra")}, nil
	})
	defaults := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		ProviderKey: json.RawMessage(`"azure"`),
		ModelKey:    json.RawMessage(`"decision-1"`),
		BaseURLKey:  json.RawMessage(`"` + srv.URL + `/providers/microsoft/v1"`),
	}, nil))
	decide(t, e, Source{Defaults: defaults}, rpc.DecideRequest{Question: "q?"})
	if path != "/providers/microsoft/v1/systemone" || bearer != "Bearer entra" {
		t.Fatalf("posted to %s with %q", path, bearer)
	}
}

func TestEachSettingIsTheRequestThenTheAriaThenTheDefaultOutfit(t *testing.T) {
	var provider string
	var model string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_ = json.Unmarshal(body["model"], &model)
		io.WriteString(w, liveNoul)
	}))
	defer srv.Close()
	e := New(func(p string) (Target, error) { provider = p; return Target{BaseURL: srv.URL}, nil })
	defaults := defaultsOf(t, "rou2", "jev-latest")
	board := defaultsOf(t, "gateway", "explabs/jev-latest")
	cases := []struct {
		src          Source
		req          rpc.DecideRequest
		wantP, wantM string
	}{
		{Source{Defaults: defaults}, rpc.DecideRequest{}, "rou2", "jev-latest"},
		{Source{Defaults: defaults, Board: board}, rpc.DecideRequest{}, "gateway", "explabs/jev-latest"},
		{Source{Defaults: defaults, Board: board}, rpc.DecideRequest{Provider: "azure", Model: "decision-1"}, "azure", "decision-1"},
		{Source{Defaults: defaults, Board: defaultsOf(t, "", "explabs/jev-latest")}, rpc.DecideRequest{}, "rou2", "explabs/jev-latest"},
	}
	for i, c := range cases {
		c.req.Question = "q?"
		decide(t, e, c.src, c.req)
		if provider != c.wantP || model != c.wantM {
			t.Errorf("case %d: provider=%q model=%q, want %q %q", i, provider, model, c.wantP, c.wantM)
		}
	}
}

type fixedToken string

func (f fixedToken) Resolve() (string, error) { return string(f), nil }
func (f fixedToken) Invalidate(string) error  { return nil }

func at(url string) *Engine {
	return New(func(string) (Target, error) { return Target{BaseURL: url}, nil })
}

func defaultsOf(t *testing.T, provider, model string) form.Snapshot {
	t.Helper()
	set := map[string]json.RawMessage{}
	if provider != "" {
		set[ProviderKey] = json.RawMessage(`"` + provider + `"`)
	}
	if model != "" {
		set[ModelKey] = json.RawMessage(`"` + model + `"`)
	}
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, set, nil))
}

var (
	jevDefaults  = form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{ProviderKey: json.RawMessage(`"test"`), ModelKey: json.RawMessage(`"jev-latest"`)}, nil))
	lunaDefaults = form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{ProviderKey: json.RawMessage(`"test"`), ModelKey: json.RawMessage(`"explabs/gpt-6-luna-decisions"`)}, nil))
)

// Cloudflare answers 524 when the origin sends no headers within 100 s
// (Spanyard, 2026-10-14, for rou2.kelliher.info), and a decision does not
// stream, so the one lever is a smaller window: the same retry a size refusal
// gets.
func TestATimeoutAtTheEdgeIsRetriedSmaller(t *testing.T) {
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sizes = append(sizes, len(body))
		if len(sizes) == 1 {
			w.WriteHeader(524)
			io.WriteString(w, "error code: 524")
			return
		}
		io.WriteString(w, liveNoul)
	}))
	defer srv.Close()
	long := make([]message.Message, 0, 200)
	for i := 0; i < 200; i++ {
		long = append(long, say(message.RoleOutput, fmt.Sprintf("%03d %s", i, strings.Repeat("tool output ", 200))))
	}
	if _, err := at(srv.URL).Decide(context.Background(), Source{Log: logOf(t, long...), Defaults: jevDefaults}, rpc.DecideRequest{Question: "q?"}); err != nil {
		t.Fatalf("no retry after a 524: %v", err)
	}
	if len(sizes) != 2 || sizes[1] >= sizes[0] {
		t.Fatalf("sizes = %v", sizes)
	}
}
