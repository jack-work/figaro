package cli

import (
	"reflect"
	"testing"
)

func TestDecideArgs(t *testing.T) {
	cases := []struct {
		args    []string
		choices []string
		q       string
	}{
		{[]string{`-C=option 1,option 2`, "--", "which?"}, []string{"option 1", "option 2"}, "which?"},
		{[]string{"-C", "a,b", "-C", "c", "--", "q"}, []string{"a", "b", "c"}, "q"},
		{[]string{"--choices=@skills", "--", "which", "skill?"}, []string{"@skills"}, "which skill?"},
		{tokenize(`-C="option 1","option 2" -- which one?`), []string{"option 1", "option 2"}, "which one?"},
		{[]string{"--", "is it?"}, nil, "is it?"},
	}
	for _, c := range cases {
		p, err := parseDecideArgs(c.args)
		if err != nil {
			t.Fatalf("%q: %v", c.args, err)
		}
		if !reflect.DeepEqual(p.choices, c.choices) && !(len(p.choices) == 0 && len(c.choices) == 0) {
			t.Errorf("%q: choices = %#v, want %#v", c.args, p.choices, c.choices)
		}
		if p.question != c.q {
			t.Errorf("%q: question = %q, want %q", c.args, p.question, c.q)
		}
	}
	if _, err := parseDecideArgs([]string{"--"}); err == nil {
		t.Error("an empty decide was accepted")
	}
	if _, err := parseDecideArgs([]string{"-x", "--", "q"}); err == nil {
		t.Error("an unknown flag was accepted")
	}
}
