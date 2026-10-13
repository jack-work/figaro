package cli

import (
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/decide"
	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

// A draft whose last line starts with ?? is a decision, on ANY aria: the
// model behind the conversation is irrelevant, because decide never writes
// to it. Everything else about the drawer is unchanged, which is what these
// fixtures hold still.

type decided struct {
	question string
	choices  []string
}

func decideTranscript(t *testing.T) (*transcript, *[]string, *[]decided) {
	t.Helper()
	tr := newTranscript(io.Discard, 80, 24, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	var sent []string
	var asked []decided
	tr.sendComposer = func(text string) { sent = append(sent, text) }
	tr.decideComposer = func(q string, c []string) { asked = append(asked, decided{q, c}) }
	tr.enter()
	return tr, &sent, &asked
}

func TestADraftEndingInAnOptionLineDecides(t *testing.T) {
	tr, sent, asked := decideTranscript(t)
	pagerComposerBox(tr)
	typeInto(tr, "which fix?")
	tr.draft.insertNewline()
	typeInto(tr, "?? ship revert wait")
	composerSubmit(tr)

	if len(*sent) != 0 {
		t.Fatalf("a decision was sent to the model as a prompt: %#v", *sent)
	}
	if len(*asked) != 1 || (*asked)[0].question != "which fix?\n?? ship revert wait" {
		t.Fatalf("asked = %#v", *asked)
	}
	if tr.box != boxNone || tr.draft.String() != "" {
		t.Fatalf("the drawer was not put away: box=%v draft=%q", tr.box, tr.draft.String())
	}
}

func TestABareSigilOpensTheOptionsLine(t *testing.T) {
	tr, sent, asked := decideTranscript(t)
	pagerComposerBox(tr)
	typeInto(tr, "which fix?")
	tr.draft.insertNewline()
	typeInto(tr, "??")
	composerSubmit(tr)

	if len(*sent)+len(*asked) != 0 {
		t.Fatalf("something went out before the options: sent=%#v asked=%#v", *sent, *asked)
	}
	if !tr.askOptions || tr.box != boxComposer {
		t.Fatalf("askOptions=%v box=%v", tr.askOptions, tr.box)
	}
	typeInto(tr, `ship it, "revert, then wait", wait`)
	if got := tr.draft.String(); got != "which fix?\n??" {
		t.Fatalf("the options line edited the draft: %q", got)
	}
	composerSubmit(tr)
	if len(*asked) != 1 {
		t.Fatalf("asked = %#v", *asked)
	}
	want := []string{"ship it", "revert, then wait", "wait"}
	if !reflect.DeepEqual((*asked)[0].choices, want) {
		t.Fatalf("choices = %#v, want %#v", (*asked)[0].choices, want)
	}
	if tr.askOptions || tr.optline.String() != "" {
		t.Fatalf("the options line outlived the decision: %v %q", tr.askOptions, tr.optline.String())
	}
}

func TestAnEmptyOptionsLineIsAYesNo(t *testing.T) {
	tr, _, asked := decideTranscript(t)
	pagerComposerBox(tr)
	typeInto(tr, "is the step size the learning rate?")
	tr.draft.insertNewline()
	typeInto(tr, "??")
	composerSubmit(tr)
	composerSubmit(tr)
	if len(*asked) != 1 || len((*asked)[0].choices) != 0 {
		t.Fatalf("asked = %#v", *asked)
	}
}

func TestEnterAcceptsTheOptionsLine(t *testing.T) {
	// One line, so Enter means accept. In the DRAFT Enter stays a newline.
	tr, _, asked := decideTranscript(t)
	pagerComposerBox(tr)
	typeInto(tr, "pick")
	tr.key('\r')
	if tr.draft.String() != "pick\n" {
		t.Fatalf("Enter in the draft is not a newline: %q", tr.draft.String())
	}
	typeInto(tr, "??")
	composerSubmit(tr)
	typeInto(tr, "a, b")
	tr.key('\r')
	if len(*asked) != 1 || !reflect.DeepEqual((*asked)[0].choices, []string{"a", "b"}) {
		t.Fatalf("asked = %#v", *asked)
	}
}

func TestADraftWithoutTheSigilIsAnOrdinaryPrompt(t *testing.T) {
	tr, sent, asked := decideTranscript(t)
	pagerComposerBox(tr)
	typeInto(tr, "what do you think?")
	composerSubmit(tr)
	if len(*asked) != 0 || len(*sent) != 1 {
		t.Fatalf("sent=%#v asked=%#v", *sent, *asked)
	}
}

func TestAnEscapedSigilIsSentLiterally(t *testing.T) {
	tr, sent, asked := decideTranscript(t)
	pagerComposerBox(tr)
	typeInto(tr, "write this down")
	tr.draft.insertNewline()
	typeInto(tr, `\?? not options`)
	composerSubmit(tr)
	if len(*asked) != 0 || len(*sent) != 1 {
		t.Fatalf("sent=%#v asked=%#v", *sent, *asked)
	}
}

func TestASigilInsideAFenceIsCode(t *testing.T) {
	tr, sent, asked := decideTranscript(t)
	pagerComposerBox(tr)
	tr.draft.set("why is this undefined?\n```ts\nconst v = a\n  ?? b\n```")
	composerSubmit(tr)
	if len(*asked) != 0 || len(*sent) != 1 {
		t.Fatalf("sent=%#v asked=%#v", *sent, *asked)
	}
}

func TestEscapeFromTheOptionsLineKeepsTheDraft(t *testing.T) {
	tr, sent, asked := decideTranscript(t)
	pagerComposerBox(tr)
	tr.draft.set("a question worth keeping\n??")
	composerSubmit(tr)
	typeInto(tr, "half typed")
	boxCancel(tr)
	if tr.askOptions || tr.box != boxComposer {
		t.Fatalf("askOptions=%v box=%v", tr.askOptions, tr.box)
	}
	if got := tr.draft.String(); got != "a question worth keeping\n??" {
		t.Fatalf("the draft was spent by Esc: %q", got)
	}
	if len(*sent)+len(*asked) != 0 {
		t.Fatal("Esc sent something")
	}
	composerSubmit(tr)
	if tr.optline.String() != "" {
		t.Fatalf("the half-typed options came back: %q", tr.optline.String())
	}
}

func TestAbortFromTheOptionsLineSpendsBoth(t *testing.T) {
	tr, _, _ := decideTranscript(t)
	pagerComposerBox(tr)
	tr.draft.set("never mind\n??")
	composerSubmit(tr)
	cmdAbort(tr)
	if tr.askOptions || tr.box != boxNone || tr.draft.String() != "" {
		t.Fatalf("^C left askOptions=%v box=%v draft=%q", tr.askOptions, tr.box, tr.draft.String())
	}
}

func TestTheOptionsLineSaysWhatItWants(t *testing.T) {
	tr, _, _ := decideTranscript(t)
	pagerComposerBox(tr)
	tr.draft.set("which way?\n??")
	composerSubmit(tr)
	title := strings.Join(tr.composerTitle(), "\n")
	for _, want := range []string{"which way?", "comma", "yes/no"} {
		if !strings.Contains(title, want) {
			t.Fatalf("%q missing from the options heading:\n%s", want, title)
		}
	}
	if !strings.Contains(tr.boxSigil(), decide.Sigil) {
		t.Fatalf("sigil = %q", tr.boxSigil())
	}
	if tr.draftCompleter() != nil {
		t.Fatal("path and @ref completion was offered for an option list")
	}
}

func TestADecisionOpensAPitWithTheWinnerSelected(t *testing.T) {
	tr, _, _ := decideTranscript(t)
	conf := 0.98
	tr.showDecision("which fix?", rpc.DecideResponse{
		Model: "jev-latest", Kind: rpc.DecideChoice, Question: "which fix?",
		Rows: []rpc.DecideRow{
			{Option: "idempotency-key", Probability: 0.99, Chosen: true},
			{Option: "nothing", Probability: 0.01},
			{Option: "queue", Probability: 0},
		},
		Confidence: &conf, InputToks: 388,
		Window: rpc.DecideWindow{Messages: 7},
	})
	if !tr.showing(pitDecision) {
		t.Fatalf("pit = %q", tr.pit.id)
	}
	row, ok := tr.pit.selected()
	if !ok || row.yank != "idempotency-key" {
		t.Fatalf("the preferred option is not the highlighted row: %+v", row)
	}
	all := strings.Join(tr.pit.lines(80, 20), "\n")
	for _, want := range []string{"which fix?", "idempotency-key", "0.99", decide.Marker, "confidence 0.98", "7 messages"} {
		if !strings.Contains(all, want) {
			t.Fatalf("%q missing from the pit:\n%s", want, all)
		}
	}
}

func TestOnlyTheNewestDecisionIsKept(t *testing.T) {
	tr, _, _ := decideTranscript(t)
	tr.showDecision("first?", rpc.DecideResponse{Question: "first?", Rows: []rpc.DecideRow{{Option: "yes", Probability: 0.9, Chosen: true}}})
	tr.showDecision("second?", rpc.DecideResponse{Question: "second?", Rows: []rpc.DecideRow{{Option: "no", Probability: 0.8, Chosen: true}}})
	panelDismiss(tr)
	tr.openDecisionPit()
	all := strings.Join(tr.pit.lines(80, 20), "\n")
	if strings.Contains(all, "first?") || !strings.Contains(all, "second?") {
		t.Fatalf("the pit does not hold exactly the newest decision:\n%s", all)
	}
}

func TestReopeningWithNoDecisionSaysHowToMakeOne(t *testing.T) {
	tr, _, _ := decideTranscript(t)
	tr.openDecisionPit()
	if tr.showing(pitDecision) {
		t.Fatal("an empty decision pit was opened")
	}
}

func TestAHighlightPlaceholderWithNoHighlightIsRefused(t *testing.T) {
	tr, _, asked := decideTranscript(t)
	pagerComposerBox(tr)
	tr.draft.set(visualRangePlaceholder + " is this right?\n?? yes no")
	composerSubmit(tr)
	if len(*asked) != 0 {
		t.Fatalf("a dangling <,> was decided: %#v", *asked)
	}
	if tr.box != boxComposer {
		t.Fatal("the draft was spent on a refusal")
	}
}

// Gluck, 2026-10-13: the answer should work like the form pit or the queue,
// where each row can be selected. Only the winner was selectable, so j and k
// had nowhere to go.
func TestEveryOptionInTheDecisionPitIsSelectable(t *testing.T) {
	tr, _, _ := decideTranscript(t)
	tr.showDecision("which fix?", rpc.DecideResponse{
		Kind: rpc.DecideChoice, Question: "which fix?",
		Rows: []rpc.DecideRow{
			{Option: "idempotency-key", Probability: 0.72, Chosen: true},
			{Option: "queue", Probability: 0.14},
			{Option: "nothing", Probability: 0.14},
		},
	})
	if tr.focused != focusPit {
		t.Fatal("the answer arrived without the focus")
	}
	if row, _ := tr.pit.selected(); row.yank != "idempotency-key" {
		t.Fatalf("the cursor does not start on the preferred option: %+v", row)
	}
	tr.key('j')
	if row, _ := tr.pit.selected(); row.yank != "queue" {
		t.Fatalf("j did not move to the next option: %+v", row)
	}
	tr.key('j')
	if row, _ := tr.pit.selected(); row.yank != "nothing" {
		t.Fatalf("y would copy %+v, want the option under the cursor", row)
	}
	tr.key('k')
	tr.key('k')
	if row, _ := tr.pit.selected(); row.yank != "idempotency-key" {
		t.Fatalf("k did not walk back to the top: %+v", row)
	}
}
