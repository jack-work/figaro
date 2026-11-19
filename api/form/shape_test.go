package form

import (
	"bytes"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"sort"
	"strings"
	"testing"
)

var updateKeyShapes = flag.Bool("update-key-shapes", false, "rewrite testdata/key_shapes.golden from the current registry")

const keyShapesGolden = "testdata/key_shapes.golden"

var shapeCorpus = []string{
	`true`, `false`, `null`, `0`, `1`, `-1`, `2`, `3`, `0.5`, `1.5`, `1e3`, `64`, `4096`,
	`""`, `" "`, `"x"`, `"true"`, `"false"`, `"yes"`, `"no"`, `"on"`, `"off"`, `"0"`, `"1"`, `"-5"`, `"1024"`,
	`"90m"`, `"2d"`, `"1w"`, `"1.5d"`, `"-5m"`, `"abc"`, `"context"`, `"none"`, `"5m"`, `"1h"`, `"long"`, `"short"`,
	`"blocks"`, `"Top-Level"`, `"auto"`, `"marks"`, `"default"`, `"long_context"`, `"openai"`, `"systemone"`,
	`"current_turn"`, `"detailed"`, `"concise"`, `"watch it"`, `"None"`, `" auto"`,
	`[]`, `["a"]`, `[" a"]`, `["a",""]`, `["read:*","set:aria:self"]`, `[1]`, `[null]`,
	`{}`, `{"onstudy":"x"}`, `{"onstudy":1}`, `{"OnStudy":"x"}`, `{" ondrop ":"x"}`, `{"onstudy":null}`,
	`{"onfork":"x"}`, `{"onstudied":"x"}`, `{"bogus":"x"}`, `{"content":"c"}`,
	`{"context_threshold":"60%"}`, `{"context_threshold":0}`, `{"context_threshold":"101%"}`,
	`{"context_threshold":1000,"ttl":"1h","action":"succeed"}`, `{"context_threshold":1000,"action":"stay"}`,
	`{"ttl":"1h"}`, `{"context_threshold":1000,"ramp_turns":"0"}`,
}

func shapeTable() string {
	var b strings.Builder
	for _, s := range registry {
		for _, v := range shapeCorpus {
			verdict := "ok"
			if s.Shape.Check(json.RawMessage(v)) != nil {
				verdict = "no"
			}
			b.WriteString(s.Path + "\t" + v + "\t" + verdict + "\n")
		}
	}
	return b.String()
}

func TestEveryRowHasAShape(t *testing.T) {
	for _, s := range registry {
		if !s.Shape.Defined() || s.Shape.Name == "" {
			t.Errorf("%s has no shape; a row with no constraint says ShapeAny()", s.Path)
		}
	}
}

func TestShapeVerdictsMatchTheGolden(t *testing.T) {
	got := shapeTable()
	if *updateKeyShapes {
		if err := os.WriteFile(keyShapesGolden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(keyShapesGolden)
	if err != nil {
		t.Fatal(err)
	}
	if got == string(want) {
		return
	}
	g, w := strings.Split(got, "\n"), strings.Split(string(want), "\n")
	wantSet := map[string]bool{}
	for _, l := range w {
		wantSet[l] = true
	}
	var diff []string
	for _, l := range g {
		if !wantSet[l] {
			diff = append(diff, l)
		}
	}
	sort.Strings(diff)
	if len(diff) > 20 {
		diff = diff[:20]
	}
	t.Fatalf("shape verdicts moved from %s (run with -update-key-shapes only when a shape is meant to change):\n%s", keyShapesGolden, strings.Join(diff, "\n"))
}

func warnsWhileReading(read func()) bool {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)
	read()
	return buf.Len() > 0
}

func TestShapesAgreeWithTheReadersThatJudgeAValue(t *testing.T) {
	readers := map[string]func(Snapshot) bool{
		StudyIncantationKey: func(s Snapshot) bool { return !warnsWhileReading(func() { ReadStudyIncantation(s) }) },
		ForkIncantationKey:  func(s Snapshot) bool { return !warnsWhileReading(func() { ReadForkIncantation(s) }) },
		ScopesKey:           func(s Snapshot) bool { return CheckScopes(s) == nil },
		GrantsKey: func(s Snapshot) bool {
			_, _, err := OwnScopes(s)
			return err == nil
		},
		FanoutMaxChildrenKey: func(s Snapshot) bool {
			_, _, err := FanoutMaxChildren(s)
			return err == nil
		},
		FanoutQuotaKey: func(s Snapshot) bool {
			_, _, err := FanoutQuota(s)
			return err == nil
		},
		FanoutMintKey: func(s Snapshot) bool {
			_, err := FanoutMint(s)
			return err == nil
		},
		RetirementKey: func(s Snapshot) bool {
			raw, _ := s.Get(RetirementKey)
			if isNull(raw) {
				return true
			}
			_, err := ParseRetirement(raw)
			return err == nil
		},
	}
	for key, accepts := range readers {
		shape, ok := ShapeOf(key)
		if !ok {
			t.Fatalf("%s has no registry row", key)
		}
		for _, v := range shapeCorpus {
			snap := FromMap(map[string]json.RawMessage{key: json.RawMessage(v)})
			if r, s := accepts(snap), shape.Check(json.RawMessage(v)) == nil; r != s {
				t.Errorf("%s = %s: the reader accepts it %v, the shape %v", key, v, r, s)
			}
		}
	}
}

func TestAPlaceholderRowGivesItsShapeToEachName(t *testing.T) {
	if s, ok := ShapeOf("system.environment.figaro_wire_dir"); !ok || s.Name != ShapeString().Name {
		t.Fatalf("system.environment.<name>: %v %v", s, ok)
	}
	if _, ok := ShapeOf("system.environment.a.b"); ok {
		t.Fatal("a placeholder matched two segments")
	}
	if _, ok := ShapeOf("mantra"); ok {
		t.Fatal("an unregistered key has a shape")
	}
}

func TestTheAuditNamesEachValueThatFailsItsShape(t *testing.T) {
	snap := FromMap(map[string]json.RawMessage{
		"system.max_tokens":           json.RawMessage(`0`),
		"system.study_incantation":    json.RawMessage(`{"onstudied":"x"}`),
		"system.environment.wire_dir": json.RawMessage(`7`),
		"system.environment.other":    json.RawMessage(`"/tmp"`),
		"system.methods.enabled":      json.RawMessage(`true`),
		"system.methods.pick":         json.RawMessage(`"not an object"`),
		"system.model":                json.RawMessage(`"claude"`),
		"mantra":                      json.RawMessage(`42`),
	})
	got := map[string]bool{}
	for _, f := range AuditShapes(snap) {
		got[f.Path] = true
	}
	want := []string{"system.max_tokens", "system.study_incantation", "system.environment.wire_dir", "system.methods.pick"}
	for _, p := range want {
		if !got[p] {
			t.Errorf("%s fails its shape and the audit did not name it", p)
		}
	}
	if len(got) != len(want) {
		t.Errorf("the audit named %v; want exactly %v", got, want)
	}
}
