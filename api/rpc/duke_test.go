package rpc

import (
	"encoding/json"
	"testing"
)

// The duke's name belongs to the aria being addressed: the CLI sends a
// placeholder and the server resolves it.
func TestDukePlaceholderIsResolvedByTheTarget(t *testing.T) {
	raw, err := WithCaller(QuaRequest{Text: "hi"}, "", &CallerRef{Duke: true})
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}

	// Unresolved, the placeholder has no name.
	if got := LabelOf(raw); got != "" {
		t.Fatalf("duke placeholder carried a label: %q", got)
	}

	if got := SenderFrom(raw, func() string { return "gluck" }); got != "gluck" {
		t.Fatalf("SenderFrom = %q, want gluck", got)
	}

	if got := SenderFrom(raw, func() string { return "" }); got != DefaultDukeTitle {
		t.Fatalf("SenderFrom = %q, want %q", got, DefaultDukeTitle)
	}
	if got := SenderFrom(raw, nil); got != DefaultDukeTitle {
		t.Fatalf("no resolver: %q, want %q", got, DefaultDukeTitle)
	}
}

// No value of FIGARO_CALLER can produce the duke flag: it only populates Label.
func TestNoLabelCanForgeTheDukeFlag(t *testing.T) {
	for _, attempt := range []string{
		"duke", `{"duke":true}`, "duke=true", `"duke"`, "true", "Duke", "DUKE",
	} {
		t.Setenv("FIGARO_CALLER", attempt)
		SetInteractive(false)
		ref := CallerRefFromEnv()
		if ref == nil {
			continue
		}
		if ref.Duke {
			t.Fatalf("FIGARO_CALLER=%q produced Duke:true", attempt)
		}
	}
}

func TestExplicitLabelOverridesTheDuke(t *testing.T) {
	raw, err := WithCaller(QuaRequest{Text: "hi"}, "", &CallerRef{Duke: true, Label: "ci-bot"})
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}
	if got := SenderFrom(raw, func() string { return "gluck" }); got != "ci-bot" {
		t.Fatalf("SenderFrom = %q, want ci-bot", got)
	}
}

// An asserted aria id renders bare: nothing on the wire proves it.
func TestAriaIdentityOutranksTheDuke(t *testing.T) {
	raw, err := WithCaller(QuaRequest{Text: "hi"}, "aria0001", &CallerRef{Duke: true})
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}
	if got := SenderFrom(raw, func() string { return "gluck" }); got != "aria0001" {
		t.Fatalf("SenderFrom = %q, want aria0001", got)
	}
}

// A figaro that allocates itself a terminal can still present the duke: an
// accepted gap, which is why none of this touches authorization.
func TestDukeRequiresInteractive(t *testing.T) {
	t.Setenv("FIGARO_CALLER", "")

	SetInteractive(false)
	if ref := CallerRefFromEnv(); ref != nil {
		t.Fatalf("non-interactive presented %+v, want nothing", ref)
	}

	SetInteractive(true)
	ref := CallerRefFromEnv()
	if ref == nil || !ref.Duke {
		t.Fatalf("interactive presented %+v, want Duke:true", ref)
	}

	t.Setenv("FIGARO_CALLER", "ci-bot")
	ref = CallerRefFromEnv()
	if ref == nil || ref.Duke || ref.Label != "ci-bot" {
		t.Fatalf("override produced %+v, want Label:ci-bot Duke:false", ref)
	}
	SetInteractive(false)
}

func TestResolvedDukeTitleIsSanitized(t *testing.T) {
	raw, _ := WithCaller(QuaRequest{Text: "hi"}, "", &CallerRef{Duke: true})
	if got := SenderFrom(raw, func() string { return "aria 999" }); got == "aria 999" {
		t.Fatalf("a form value impersonated an aria: %q", got)
	}
	if got := SenderFrom(raw, func() string { return "a\nb" }); got != "ab" {
		t.Fatalf("control chars survived a form title: %q", got)
	}
}

func TestNoRefAddsNothingToParams(t *testing.T) {
	raw, err := WithCaller(QuaRequest{Text: "hi"}, "", nil)
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("params: %v", err)
	}
	if _, present := fields[CallerLabelKey]; present {
		t.Fatalf("%s present with no caller: %s", CallerLabelKey, raw)
	}
	raw, _ = WithCaller(QuaRequest{Text: "hi"}, "", &CallerRef{})
	_ = json.Unmarshal(raw, &fields)
	if _, present := fields[CallerLabelKey]; present {
		t.Fatalf("empty ref reached the wire: %s", raw)
	}
}
