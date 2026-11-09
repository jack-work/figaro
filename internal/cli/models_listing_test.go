package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/models"
)

var modelsNow = time.UnixMilli(1_790_000_000_000)

func catalogFixture(t *testing.T, provs map[string]models.Provider) form.Snapshot {
	t.Helper()
	set := map[string]json.RawMessage{}
	for name, p := range provs {
		p.IR = models.IRVersion
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		set[name] = raw
	}
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, set, nil))
}

func modelsFixture(t *testing.T) form.Snapshot {
	listed := modelsNow.Add(-2 * time.Hour).UnixMilli()
	return catalogFixture(t, map[string]models.Provider{
		"anthropic": {RefreshedAt: listed, Models: map[string]models.Entry{
			"claude-opus-5":     {Name: "Claude Opus 5", Context: models.Context{Default: 1_000_000}, MaxOutput: 32000},
			"claude-sonnet-4-5": {Name: "Claude Sonnet 4.5", Context: models.Context{Default: 200_000}, MaxOutput: 64000},
		}},
		"copilot": {RefreshedAt: listed, Models: map[string]models.Entry{
			"gpt-5":           {Name: "GPT-5", Context: models.Context{Default: 400_000}},
			"claude-opus-5":   {Name: "Claude Opus 5", Context: models.Context{Default: 128_000}, MaxOutput: 16000},
			"internal-router": {Context: models.Context{Default: 8_000}, Hidden: true},
		}},
	})
}

func TestTabulateAlignsAndClips(t *testing.T) {
	rows := [][]string{{"a", "long-name-here", "1k"}, {"bbb", "x", "200k"}}
	cols := []tableCol{{}, {shrink: 1}, {right: true}}
	got := tabulate(rows, cols, 0)
	want := []string{"a    long-name-here    1k", "bbb  x               200k"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	narrow := tabulate(rows, cols, 20)
	for _, l := range narrow {
		if runewidth.StringWidth(l) > 20 {
			t.Fatalf("%q exceeds 20", l)
		}
		if !strings.HasSuffix(l, "k") {
			t.Fatalf("the shrinkable column must give way before the right-aligned one: %q", l)
		}
	}
}

func TestPrintModelsShowsEveryProvidersStateThenTheTable(t *testing.T) {
	snap := catalogFixture(t, map[string]models.Provider{
		"anthropic": {AttemptedAt: modelsNow.Add(-3 * time.Minute).UnixMilli(), Error: "no credential"},
		"copilot": {RefreshedAt: modelsNow.Add(-2 * time.Hour).UnixMilli(), Models: map[string]models.Entry{
			"gpt-5":   {Name: "OpenAI GPT-5", Context: models.Context{Default: 400_000}, Src: map[string]string{"context": "api", "name": "api"}},
			"gpt-6":   {Context: models.Context{Default: 1_000_000}, MaxPrompt: 872_000, MaxOutput: 64_000, Src: map[string]string{"context": "api", "max_output": "config"}},
			"private": {},
		}},
	})
	var buf bytes.Buffer
	old := stdout
	stdout = &buf
	defer func() { stdout = old }()
	printModels(catalogListings(snap, modelsNow), modelsNow)
	want := strings.Join([]string{
		"anthropic  failed 3m ago: no credential",
		"copilot    listed 2h ago",
		"",
		"PROVIDER  MODEL    NAME          CONTEXT  PROMPT  MAX OUT  SRC",
		"copilot   gpt-5    OpenAI GPT-5     400k       -        -  api",
		"copilot   gpt-6                     1.0m    872k      64k  api+config",
		"copilot   private                      -       -        -  -",
		"",
	}, "\n")
	if buf.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", buf.String(), want)
	}
}

// A model's id, name and a provider's error come from the provider: a name
// carrying an escape sequence must print as text, never reach the terminal.
func TestPrintModelsSanitisesWhatAProviderSays(t *testing.T) {
	snap := catalogFixture(t, map[string]models.Provider{
		"evil": {RefreshedAt: modelsNow.Add(-time.Minute).UnixMilli(), Models: map[string]models.Entry{
			"m\x1b[2Jx": {Name: "Nice\x1b]52;c;cGF3bmVk\x07 Name\x1b[31m", Context: models.Context{Default: 1000}},
		}},
		"broken": {AttemptedAt: modelsNow.Add(-time.Minute).UnixMilli(), Error: "denied\x1b[2J\r\nfake prompt$ "},
	})
	var buf bytes.Buffer
	old := stdout
	stdout = &buf
	defer func() { stdout = old }()
	printModels(catalogListings(snap, modelsNow), modelsNow)
	out := buf.String()
	if strings.ContainsAny(out, "\x1b\x07\r") {
		t.Fatalf("control bytes reached the terminal: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "fake prompt") {
			t.Errorf("a provider's error broke the line: %q", out)
		}
	}
	for _, want := range []string{"Nice", "Name", "denied"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q lost its text: %q", want, out)
		}
	}
}
