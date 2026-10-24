package cli

import (
	"context"
	"strings"
	"testing"
	"time"
)

// o opens what y would copy: the selected messages, or the highlight.
func TestOpenEditorOpensTheSelection(t *testing.T) {
	reader := &stubHistoryReader{history: transcriptHistory(12)}
	tc := newRecordingTerminal()
	in := newSearchInteractiveInput(reader, tc.searchInputTerminal)
	in.tc = tc

	if _, ok := in.editorSource(); ok {
		t.Fatal("with nothing selected, o opened something")
	}

	in.mu.Lock()
	in.lt.tr.selectNode(-1, false)
	plan, _ := in.lt.transcriptSelectionPlan()
	in.mu.Unlock()
	source, ok := in.editorSource()
	if !ok {
		t.Fatal("a selected message did not open")
	}
	got, err := source(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want, err := selectionText(plan, transcriptPageSize, nil)
	if err != nil || got != want || got == "" {
		t.Fatalf("o opened %q, y would copy %q (%v)", got, want, err)
	}

	in.mu.Lock()
	tr := in.lt.tr
	tr.clearSelection()
	tr.key('v')
	tr.key('v')
	tr.key('l')
	tr.key('l')
	highlight := tr.visualText()
	in.mu.Unlock()
	source, ok = in.editorSource()
	if !ok {
		t.Fatal("a highlight did not open")
	}
	if got, _ := source(context.Background()); got != highlight || strings.TrimSpace(got) == "" {
		t.Fatalf("o opened %q, the highlight is %q", got, highlight)
	}
}

func TestTheEditorFallsBackToTheEnvironment(t *testing.T) {
	in := &interactiveInput{}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "vi")
	if got := in.editorSpec(); got != "vi" {
		t.Errorf("with only $EDITOR: %q", got)
	}
	t.Setenv("VISUAL", "nvim")
	if got := in.editorSpec(); got != "nvim" {
		t.Errorf("$VISUAL comes first: %q", got)
	}
}

// Ctrl+Enter reaches figaro only as a CSI-u report, and folds onto Alt+Enter.
func TestCtrlEnterInVisualOpensThePath(t *testing.T) {
	reader := &stubHistoryReader{history: transcriptHistory(12)}
	tc := newRecordingTerminal()
	in := newSearchInteractiveInput(reader, tc.searchInputTerminal)
	in.tc = tc
	opened := make(chan string, 1)
	in.mu.Lock()
	in.lt.tr.openPath = func(s string) { opened <- s }
	in.lt.tr.key('v')
	in.lt.tr.key('w')
	want := in.lt.tr.visualTextUnderCursor()
	in.mu.Unlock()
	if want == "" {
		t.Fatal("fixture: no word under the visual cursor")
	}

	done := make(chan struct{})
	go func() { in.run(); close(done) }()
	tc.send([]byte("\x1b[13;5u"))
	select {
	case got := <-opened:
		if got != want {
			t.Errorf("Ctrl+Enter opened %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ctrl+Enter in visual mode did not open the path under the cursor")
	}
	tc.send([]byte{0x04})
	waitSignal(t, done, "input loop exit")
}
