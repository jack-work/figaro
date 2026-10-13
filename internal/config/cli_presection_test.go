package config

import "testing"

func TestPreSectionCLIKeysStillApply(t *testing.T) {
	l, err := loadWith(t, "default_outfit = \"default\"\necho_prompt = false\nstream_cps = 40\n")
	if err != nil {
		t.Fatalf("a pre-[cli] config must still load: %v", err)
	}
	if l.EchoPrompt() {
		t.Error("top-level echo_prompt = false was ignored")
	}
	if got := l.StreamCPS(); got != 40 {
		t.Errorf("top-level stream_cps = 40 was ignored (got %d)", got)
	}
}

func TestSectionBeatsPreSection(t *testing.T) {
	l, err := loadWith(t, "echo_prompt = false\nstream_cps = 40\n[cli]\necho_prompt = true\n")
	if err != nil {
		t.Fatal(err)
	}
	if !l.EchoPrompt() {
		t.Error("[cli] echo_prompt = true must beat the top-level false")
	}
	if got := l.StreamCPS(); got != 40 {
		t.Errorf("[cli] said nothing about stream_cps; the old spelling should still fill it (got %d)", got)
	}
}
