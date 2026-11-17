package routed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

func sentMaxTokens(t *testing.T, knobs provider.Knobs, entry models.Entry, history string) int {
	t.Helper()
	return sentMaxTokensFor(t, knobs, entry, []message.Message{prompt(history)})
}

func prompt(text string) message.Message {
	return message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent(text)}}
}

func reply(input int) message.Message {
	return message.Message{Role: message.RoleOutput, StopReason: message.StopEnd,
		Usage: &message.Usage{InputTokens: input}, Content: []message.Content{message.TextContent("ok")}}
}

func sentMaxTokensFor(t *testing.T, knobs provider.Knobs, entry models.Entry, rows []message.Message) int {
	t.Helper()
	sent := -1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens *int `json:"max_completion_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.MaxTokens != nil {
			sent = *body.MaxTokens
		} else {
			sent = 0
		}
		http.Error(w, "stop here", http.StatusTeapot)
	}))
	defer srv.Close()
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, err := figLog.Append(arialog.Entry[message.Message]{Payload: row}); err != nil {
			t.Fatal(err)
		}
	}
	knobs.Model = "big"
	p, err := Build(provider.BuildContext{
		Knobs: knobs, Resolver: staticToken("k"),
		OpenTranslator: func(aria, ns string) (arialog.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) },
	}, Spec{Name: "max-out", BaseURL: func() string { return srv.URL }, Default: Chat}, "k")
	if err != nil {
		t.Fatal(err)
	}
	p.cat = newCatalogCache(map[string]models.Entry{"big": entry})
	in := provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: knobs.MaxTokens,
		Snapshot: form.FromValue(form.NewValue([]byte(`{"system":{"model":"big"}}`)))}
	_ = p.Send(context.Background(), in, &captureBus{})
	if sent < 0 {
		t.Fatal("no request reached the gateway")
	}
	return sent
}

// #80: the gateway default of 16000 cut a 128k-output model's tool call.
func TestTheDefaultMaxTokensIsTheCatalogMaxOutput(t *testing.T) {
	got := sentMaxTokens(t, provider.Knobs{}, models.Entry{MaxOutput: 128000, Context: models.Context{Default: 1000000}}, "hi")
	if got != 128000 {
		t.Fatalf("max tokens = %d, want the catalog's 128000", got)
	}
}

func TestAnUnknownMaxOutputFallsBackToTheFloor(t *testing.T) {
	got := sentMaxTokens(t, provider.Knobs{}, models.Entry{}, "hi")
	if got != 16000 {
		t.Fatalf("max tokens = %d, want the 16000 floor", got)
	}
}

func TestAnExplicitMaxTokensWins(t *testing.T) {
	got := sentMaxTokens(t, provider.Knobs{MaxTokens: 64}, models.Entry{MaxOutput: 128000}, "hi")
	if got != 64 {
		t.Fatalf("max tokens = %d, want the configured 64", got)
	}
}

// Prompt plus max_tokens past the window is a 400 on most gateways.
func TestTheDefaultLeavesRoomForThePromptInTheWindow(t *testing.T) {
	history := strings.Repeat("abcd", 100000)
	got := sentMaxTokens(t, provider.Knobs{}, models.Entry{MaxOutput: 128000, Context: models.Context{Default: 200000}}, history)
	if got <= 0 || got+100000 > 200000 {
		t.Fatalf("max tokens = %d with a ~100k prompt in a 200k window", got)
	}
	if got < 16000 {
		t.Fatalf("max tokens = %d, below the floor though the window has room", got)
	}
}

var bigWindow = models.Entry{MaxOutput: 128000, Context: models.Context{Default: 200000}}

// The last reply's usage is the prompt's size; "abcd" after it is one token.
// 200000 - (100000 + 1) - 200000/20 leaves 89999.
func TestTheDefaultCountsThePromptFromTheLastUsageAndKeepsAMargin(t *testing.T) {
	got := sentMaxTokensFor(t, provider.Knobs{}, bigWindow, []message.Message{prompt("hi"), reply(100000), prompt("abcd")})
	if got != 89999 {
		t.Fatalf("max tokens = %d, want 89999", got)
	}
}

func TestTheUsageWatermarkIsFoundPastTheFirstPage(t *testing.T) {
	rows := []message.Message{prompt("hi"), reply(100000)}
	for range 3 * usageLookback {
		rows = append(rows, prompt("abcd"))
	}
	got := sentMaxTokensFor(t, provider.Knobs{}, bigWindow, rows)
	if want := 200000 - (100000 + 3*usageLookback) - 10000; got != want {
		t.Fatalf("max tokens = %d, want %d", got, want)
	}
}

func TestAPromptThatFillsTheWindowStillGetsTheFloor(t *testing.T) {
	full := []message.Message{prompt("hi"), reply(199000), prompt("abcd")}
	if got := sentMaxTokensFor(t, provider.Knobs{}, bigWindow, full); got != 4096 {
		t.Fatalf("max tokens = %d, want the 4096 floor", got)
	}
	small := models.Entry{MaxOutput: 2000, Context: models.Context{Default: 200000}}
	if got := sentMaxTokensFor(t, provider.Knobs{}, small, full); got != 2000 {
		t.Fatalf("max tokens = %d, the floor must not pass the model's 2000", got)
	}
}

type pagingLog struct {
	arialog.Log[message.Message]
	rows      int
	readWhole bool
}

func (l *pagingLog) Read() []arialog.Entry[message.Message] {
	l.readWhole = true
	return l.Log.Read()
}

func (l *pagingLog) ReadPage(from, before uint64, n int) ([]arialog.Entry[message.Message], int) {
	page, total := l.Log.ReadPage(from, before, n)
	l.rows += len(page)
	return page, total
}

// A gateway that never reports usage must not cost a read of the whole history per request.
func TestTheEstimateStopsReadingOnceThePromptPassesTheWindow(t *testing.T) {
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	for range 200 {
		if _, err := figLog.Append(arialog.Entry[message.Message]{Payload: prompt(strings.Repeat("abcd", 1000))}); err != nil {
			t.Fatal(err)
		}
	}
	log := &pagingLog{Log: figLog}
	got := promptEstimate(provider.SendInput{FigLog: log}, 10000)
	if got < 10000 {
		t.Fatalf("estimate = %d, want at least the window", got)
	}
	if log.readWhole || log.rows > 2*usageLookback {
		t.Fatalf("read %d rows (whole log: %v) to price a prompt past a 10000 window", log.rows, log.readWhole)
	}
}
