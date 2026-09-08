package form

// What an incantation does with a value that is not what it wanted.
//
// The rule under test is asymmetric on purpose: a malformed incantation costs
// its own phrase and nothing else. The block it decorates still renders, the
// coherent fields beside it still speak, and the turn still runs. A strict
// parse would mean one typo in a shared outfit silently breaking every aria
// wearing it, discovered as "the model stopped mentioning forks" weeks later.

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func boardWith(t *testing.T, key, rawValue string) Snapshot {
	t.Helper()
	return FromMap(map[string]json.RawMessage{key: json.RawMessage(rawValue)})
}

// captureWarnings redirects slog for the duration of fn and returns what was
// logged. The warnings ARE the contract here: a value we drop must say so.
func captureWarnings(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

func TestStudyIncantationReadsEveryEvent(t *testing.T) {
	board := boardWith(t, StudyIncantationKey,
		`{"onstudy":"watch it","onupdate":"it moved","ondrop":"look away"}`)
	got := ReadStudyIncantation(board)
	if got.OnStudy != "watch it" || got.OnUpdate != "it moved" || got.OnDrop != "look away" {
		t.Fatalf("all three events should carry their phrase, got %+v", got)
	}
	if got.IsEmpty() {
		t.Fatal("a populated incantation reports itself empty")
	}
}

func TestStudyIncantationAbsentIsSilentAndEmpty(t *testing.T) {
	out := captureWarnings(t, func() {
		if got := ReadStudyIncantation(Snapshot{}); !got.IsEmpty() {
			t.Fatalf("no key should read as no incantation, got %+v", got)
		}
	})
	if out != "" {
		t.Fatalf("an absent key must not warn; logged: %s", out)
	}
}

func TestStudyIncantationPartialSurvivesOneBadField(t *testing.T) {
	board := boardWith(t, StudyIncantationKey,
		`{"onstudy":"watch it","onupdate":42,"ondrop":"look away"}`)
	var got StudyIncantation
	out := captureWarnings(t, func() { got = ReadStudyIncantation(board) })

	if got.OnStudy != "watch it" || got.OnDrop != "look away" {
		t.Fatalf("one bad field silenced its neighbours: %+v", got)
	}
	if got.OnUpdate != "" {
		t.Fatalf("a non-string field must be dropped, got %q", got.OnUpdate)
	}
	if !strings.Contains(out, "onupdate") || !strings.Contains(out, "number") {
		t.Fatalf("the warning must name the field and what it found; logged: %s", out)
	}
}

func TestStudyIncantationWrongShapeIsRefusedWholesale(t *testing.T) {
	for _, tc := range []struct{ name, raw, kind string }{
		{"array", `["watch it"]`, "array"},
		{"string", `"watch it"`, "string"},
		{"number", `7`, "number"},
		{"boolean", `true`, "boolean"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got StudyIncantation
			out := captureWarnings(t, func() {
				got = ReadStudyIncantation(boardWith(t, StudyIncantationKey, tc.raw))
			})
			if !got.IsEmpty() {
				t.Fatalf("a %s must not parse as an incantation: %+v", tc.name, got)
			}
			if !strings.Contains(out, tc.kind) {
				t.Fatalf("the warning must name the kind found (%s); logged: %s", tc.kind, out)
			}
			if !strings.Contains(out, StudyIncantationKey) {
				t.Fatalf("the warning must name the key; logged: %s", out)
			}
		})
	}
}

// A key nobody reads is the hardest failure to debug: the value is there, the
// spelling looks right, and nothing happens. Name it.
func TestStudyIncantationNamesUnknownKeys(t *testing.T) {
	board := boardWith(t, StudyIncantationKey, `{"onstudied":"watch it"}`)
	var got StudyIncantation
	out := captureWarnings(t, func() { got = ReadStudyIncantation(board) })
	if !got.IsEmpty() {
		t.Fatalf("an unknown field must not become a phrase: %+v", got)
	}
	if !strings.Contains(out, "onstudied") || !strings.Contains(out, "onstudy") {
		t.Fatalf("the warning must name the typo and the known keys; logged: %s", out)
	}
}

func TestForkIncantationAcceptsBothSpellings(t *testing.T) {
	bare := ReadForkIncantation(boardWith(t, ForkIncantationKey, `"you are a branch"`))
	object := ReadForkIncantation(boardWith(t, ForkIncantationKey, `{"onfork":"you are a branch"}`))
	if bare.OnFork != "you are a branch" || object.OnFork != "you are a branch" {
		t.Fatalf("both spellings must mean the same thing: bare=%+v object=%+v", bare, object)
	}
}

func TestForkIncantationWrongShapeIsRefused(t *testing.T) {
	var got ForkIncantation
	out := captureWarnings(t, func() {
		got = ReadForkIncantation(boardWith(t, ForkIncantationKey, `["a","b"]`))
	})
	if !got.IsEmpty() {
		t.Fatalf("an array must not parse: %+v", got)
	}
	if !strings.Contains(out, "array") {
		t.Fatalf("the warning must name the kind; logged: %s", out)
	}
}

// Whitespace-only is not a phrase. It would render an empty `say` field, which
// says less than nothing: it tells the model there was something to say.
func TestIncantationTrimsAndDropsBlank(t *testing.T) {
	s := ReadStudyIncantation(boardWith(t, StudyIncantationKey, `{"onstudy":"   "}`))
	if !s.IsEmpty() {
		t.Fatalf("a blank phrase must be no phrase: %+v", s)
	}
	f := ReadForkIncantation(boardWith(t, ForkIncantationKey, `"  branched  "`))
	if f.OnFork != "branched" {
		t.Fatalf("a phrase must be trimmed, got %q", f.OnFork)
	}
}
