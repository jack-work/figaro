package tui

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// The fallback prompts are the ones that run where the TUI cannot, and
// they are also the only half of the passphrase UX a test can drive
// without a pty. Everything asserted here about wording is asserted
// again on the huh screens by the tmux smoke cases.

// scriptedReads makes readPassword hand back the given answers in
// order, and captures what the prompt printed. It restores the package
// variables when the test ends.
func scriptedReads(t *testing.T, answers ...string) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	oldOut, oldRead, oldTTY := promptOut, readPassword, stdinIsTTY
	t.Cleanup(func() { promptOut, readPassword, stdinIsTTY = oldOut, oldRead, oldTTY })

	promptOut = &out
	stdinIsTTY = func() bool { return true }
	i := 0
	readPassword = func() ([]byte, error) {
		if i >= len(answers) {
			t.Fatalf("prompt asked %d times, script has %d answers", i+1, len(answers))
		}
		a := answers[i]
		i++
		return []byte(a), nil
	}
	return &out
}

func TestCreateFallbackAsksTwiceAndReturnsTheAgreedPassphrase(t *testing.T) {
	scriptedReads(t, "hunter2", "hunter2")
	pp, err := PromptPassphrase(PassphraseRequest{App: "figaro", Mode: PassphraseCreate})
	if err != nil {
		t.Fatalf("PromptPassphrase: %v", err)
	}
	if string(pp) != "hunter2" {
		t.Fatalf("got %q, want %q", pp, "hunter2")
	}
}

func TestCreateFallbackReasksOnMismatch(t *testing.T) {
	out := scriptedReads(t, "hunter2", "hunter3", "hunter2", "hunter2")
	pp, err := PromptPassphrase(PassphraseRequest{App: "figaro", Mode: PassphraseCreate})
	if err != nil {
		t.Fatalf("PromptPassphrase: %v", err)
	}
	if string(pp) != "hunter2" {
		t.Fatalf("got %q, want %q", pp, "hunter2")
	}
	if !strings.Contains(out.String(), "do not match") {
		t.Fatalf("expected a mismatch message, got:\n%s", out)
	}
}

// The keyring promise is the sentence that was false on the machine
// this whole change came from.
func TestCreateFallbackPromisesTheKeyringOnlyWhenThereIsOne(t *testing.T) {
	out := scriptedReads(t, "hunter2", "hunter2")
	if _, err := PromptPassphrase(PassphraseRequest{App: "figaro", Mode: PassphraseCreate, SavesToKeyring: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "OS keyring") || !strings.Contains(out.String(), "won't be asked again") {
		t.Fatalf("with a keyring, say so:\n%s", out)
	}

	out = scriptedReads(t, "hunter2", "hunter2")
	if _, err := PromptPassphrase(PassphraseRequest{App: "figaro", Mode: PassphraseCreate}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "won't be asked again") {
		t.Fatalf("without a keyring, promise nothing:\n%s", out)
	}
	if !strings.Contains(out.String(), "vault init --file") {
		t.Fatalf("without a keyring, name the way out:\n%s", out)
	}
}

func TestUnlockFallbackAsksOnceAndNeverSaysChoose(t *testing.T) {
	out := scriptedReads(t, "hunter2")
	pp, err := PromptPassphrase(PassphraseRequest{
		App:    "figaro",
		Mode:   PassphraseUnlock,
		Verify: func([]byte) error { return nil },
	})
	if err != nil {
		t.Fatalf("PromptPassphrase: %v", err)
	}
	if string(pp) != "hunter2" {
		t.Fatalf("got %q, want %q", pp, "hunter2")
	}
	// One field: no confirm, and none of the first-run wording that
	// told a returning user to invent a new passphrase.
	if strings.Contains(out.String(), "Confirm") {
		t.Fatalf("unlock must not confirm:\n%s", out)
	}
	for _, forbidden := range []string{"Choose a passphrase", "First-time setup", "keyring"} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatalf("unlock screen still says %q:\n%s", forbidden, out)
		}
	}
	if !strings.Contains(out.String(), "Unlock") {
		t.Fatalf("unlock screen should say what it is:\n%s", out)
	}
}

func TestUnlockFallbackRetriesUntilTheAnswerVerifies(t *testing.T) {
	out := scriptedReads(t, "wrong", "alsowrong", "right")
	pp, err := PromptPassphrase(PassphraseRequest{
		App:  "figaro",
		Mode: PassphraseUnlock,
		Verify: func(pp []byte) error {
			if string(pp) == "right" {
				return nil
			}
			return errors.New("age: incorrect passphrase")
		},
	})
	if err != nil {
		t.Fatalf("PromptPassphrase: %v", err)
	}
	if string(pp) != "right" {
		t.Fatalf("got %q, want %q", pp, "right")
	}
	if n := strings.Count(out.String(), "incorrect passphrase"); n != 2 {
		t.Fatalf("expected two rejections, got %d:\n%s", n, out)
	}
}

func TestUnlockFallbackGivesUpAfterThreeTries(t *testing.T) {
	out := scriptedReads(t, "a", "b", "c")
	_, err := PromptPassphrase(PassphraseRequest{
		App:    "figaro",
		Mode:   PassphraseUnlock,
		Verify: func([]byte) error { return errors.New("age: incorrect passphrase") },
	})
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("err = %v, want ErrTooManyAttempts", err)
	}
	// The user sees "incorrect passphrase" and nothing about age,
	// wrapping, or armored files.
	if strings.Contains(err.Error(), "age:") {
		t.Fatalf("the crypto error leaked into the message: %v", err)
	}
	if strings.Contains(out.String(), "try again") && strings.Count(out.String(), "try again") > 2 {
		t.Fatalf("asked more times than it said it would:\n%s", out)
	}
}

func TestUnlockFallbackHonorsTries(t *testing.T) {
	scriptedReads(t, "a")
	_, err := PromptPassphrase(PassphraseRequest{
		App:    "figaro",
		Mode:   PassphraseUnlock,
		Tries:  1,
		Verify: func([]byte) error { return errors.New("nope") },
	})
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("err = %v, want ErrTooManyAttempts", err)
	}
}

// Verify may consume the buffer it is handed (identity.Unlock zeroes
// the passphrase it decrypts with), so the prompt must hand it a copy
// or return NUL bytes to the caller.
func TestUnlockFallbackSurvivesADestructiveVerify(t *testing.T) {
	scriptedReads(t, "hunter2")
	pp, err := PromptPassphrase(PassphraseRequest{
		App:  "figaro",
		Mode: PassphraseUnlock,
		Verify: func(pp []byte) error {
			for i := range pp {
				pp[i] = 0
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(pp) != "hunter2" {
		t.Fatalf("got %q, want %q: verify ate the passphrase", pp, "hunter2")
	}
}

func TestNoTTYSaysWhichQuestionItCouldNotAsk(t *testing.T) {
	oldTTY := stdinIsTTY
	t.Cleanup(func() { stdinIsTTY = oldTTY })
	stdinIsTTY = func() bool { return false }

	_, err := promptPassphraseFallback(PassphraseRequest{App: "figaro", Mode: PassphraseUnlock})
	if err == nil || !strings.Contains(err.Error(), "unlock figaro vault") {
		t.Fatalf("unlock err = %v", err)
	}
	_, err = promptPassphraseFallback(PassphraseRequest{App: "figaro", Mode: PassphraseCreate})
	if err == nil || !strings.Contains(err.Error(), "set up figaro secrets vault") {
		t.Fatalf("create err = %v", err)
	}
}
