package rpc

import (
	"encoding/json"
	"testing"
)

// THE DUKE IS THE END USER, AND THE CLI CANNOT NAME THEM.
//
// The name belongs to the aria being addressed, not to the shell doing the
// addressing — which is what keeps it out of shell config entirely. So the CLI
// sends a PLACEHOLDER and the server resolves it.
func TestDukePlaceholderIsResolvedByTheTarget(t *testing.T) {
	raw, err := WithCaller(QuaRequest{Text: "hi"}, "", &CallerRef{Duke: true})
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}

	// Unresolved, the placeholder has NO name — LabelOf must not invent one.
	if got := LabelOf(raw); got != "" {
		t.Fatalf("duke placeholder carried a label: %q", got)
	}

	// The target names it.
	if got := SenderFrom(raw, func() string { return "gluck" }); got != "gluck" {
		t.Fatalf("SenderFrom = %q, want gluck", got)
	}

	// An aria whose form says nothing falls back to the generic default.
	// A WRONG name is worse than a plain one.
	if got := SenderFrom(raw, func() string { return "" }); got != DefaultDukeTitle {
		t.Fatalf("SenderFrom = %q, want %q", got, DefaultDukeTitle)
	}
	if got := SenderFrom(raw, nil); got != DefaultDukeTitle {
		t.Fatalf("no resolver: %q, want %q", got, DefaultDukeTitle)
	}
}

// THE PLACEHOLDER IS STRUCTURALLY UNREACHABLE FROM A STRING.
//
// A reserved word would eventually be typed by someone. A separate bool cannot
// be produced by any value of FIGARO_CALLER, because that variable only ever
// populates Label. The guarantee is a type, not a spelling.
func TestNoLabelCanForgeTheDukeFlag(t *testing.T) {
	for _, attempt := range []string{
		// A NUL is not in the list: an environment variable cannot contain
		// one, so that attack does not exist at this layer.
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

// FIGARO_CALLER is an OVERRIDE: a caller that named itself has said something
// the target aria's form cannot know.
func TestExplicitLabelOverridesTheDuke(t *testing.T) {
	raw, err := WithCaller(QuaRequest{Text: "hi"}, "", &CallerRef{Duke: true, Label: "ci-bot"})
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}
	if got := SenderFrom(raw, func() string { return "gluck" }); got != "ci-bot" {
		t.Fatalf("SenderFrom = %q, want ci-bot", got)
	}
}

// An authenticated aria outranks both. A figaro speaking is never the duke.
func TestAriaIdentityOutranksTheDuke(t *testing.T) {
	raw, err := WithCaller(QuaRequest{Text: "hi"}, "aria0001", &CallerRef{Duke: true})
	if err != nil {
		t.Fatalf("WithCaller: %v", err)
	}
	if got := SenderFrom(raw, func() string { return "gluck" }); got != "aria aria0001" {
		t.Fatalf("SenderFrom = %q, want 'aria aria0001'", got)
	}
}

// ONLY AN INTERACTIVE PROCESS PRESENTS THE DUKE. An aria's shell-out is never a
// TTY, so it cannot speak as its master by accident. (A figaro that deliberately
// allocates itself a terminal still can; that is a known, accepted gap, and it
// is why none of this touches an authorization decision.)
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

	// The override still wins, and suppresses the placeholder — a script that
	// names itself is not the duke.
	t.Setenv("FIGARO_CALLER", "ci-bot")
	ref = CallerRefFromEnv()
	if ref == nil || ref.Duke || ref.Label != "ci-bot" {
		t.Fatalf("override produced %+v, want Label:ci-bot Duke:false", ref)
	}
	SetInteractive(false)
}

// A resolved duke title is sanitized like any other label: it reaches the model
// and the terminal, and an outfit is as capable of holding a newline as an env
// var is.
func TestResolvedDukeTitleIsSanitized(t *testing.T) {
	raw, _ := WithCaller(QuaRequest{Text: "hi"}, "", &CallerRef{Duke: true})
	if got := SenderFrom(raw, func() string { return "aria 999" }); got == "aria 999" {
		t.Fatalf("a form value impersonated an aria: %q", got)
	}
	if got := SenderFrom(raw, func() string { return "a\nb" }); got != "ab" {
		t.Fatalf("control chars survived a form title: %q", got)
	}
}

// Nothing on the wire when nobody is named, so an unattributed call looks
// exactly as it did before any of this existed.
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
	// An empty ref is the same as none.
	raw, _ = WithCaller(QuaRequest{Text: "hi"}, "", &CallerRef{})
	_ = json.Unmarshal(raw, &fields)
	if _, present := fields[CallerLabelKey]; present {
		t.Fatalf("empty ref reached the wire: %s", raw)
	}
}
