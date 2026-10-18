package decide

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// What rou2's /systemone answered for explabs/jev-latest on 2026-10-18,
// verbatim: a score keys its probabilities by level index and names the
// levels in a legend.
const liveScore = `{"id":"decision_556f199eba3aaa6308f1729ba143b389","model":"jev-latest",` +
	`"answers":{"decision":{"type":"score","score":2.8,"confidence":0.81,` +
	`"legend":{"0":"low","1":"medium","2":"high","3":"xhigh"},` +
	`"probabilities":{"0":0.0,"1":0.0,"2":0.19,"3":0.81}}},` +
	`"usage":{"input_tokens":328,"output_tokens":17,"cost":0.0,"is_byok":false},"provider":"typesafe"}`

// The same day, luna asked a choice over the same four levels.
const liveLunaLevels = `{"model":"gpt-6-luna","answers":[{"type":"choice","name":"decision","choice":"low",` +
	`"probabilities":[{"value":"low","probability":0.97},{"value":"medium","probability":0.03},` +
	`{"value":"high","probability":0.0},{"value":"xhigh","probability":0.0}],"confidence":0.96}],` +
	`"usage":{"input_tokens":128,"output_tokens":0,"total_tokens":128}}`

var effortLevels = []string{"low", "medium", "high", "xhigh"}

func TestAScoreSendsItsLevelsInOrder(t *testing.T) {
	e, got := engineReplying(t, liveScore)
	out := decide(t, e, Source{Defaults: jevDefaults}, rpc.DecideRequest{
		Question: "how hard is this?", Choices: effortLevels, Score: true,
	})
	var sent struct {
		Questions map[string]struct {
			Type     string   `json:"type"`
			Criteria []string `json:"criteria"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(got.raw, &sent); err != nil {
		t.Fatalf("criteria are not a list: %s", got.raw)
	}
	q := sent.Questions[questionKey]
	if q.Type != "score" || len(q.Criteria) != 4 || q.Criteria[0] != "low" || q.Criteria[3] != "xhigh" {
		t.Fatalf("question = %+v", q)
	}
	if out.Kind != rpc.DecideScore || out.Score == nil || *out.Score != 2.8 {
		t.Fatalf("out = %+v", out)
	}
	// Levels stay in scale order; the legend maps the index keys back.
	if out.Rows[3].Option != "xhigh" || !out.Rows[3].Chosen || out.Rows[3].Probability != 0.81 || out.Rows[2].Probability != 0.19 {
		t.Fatalf("rows = %+v", out.Rows)
	}
}

func TestLunaScoresByWeightingItsChoice(t *testing.T) {
	e, got := openAIReplying(t, liveLunaLevels)
	out := decide(t, e, Source{Defaults: lunaDefaults}, rpc.DecideRequest{
		Question: "how hard is this?", Choices: effortLevels, Score: true,
	})
	if got.body.Questions[0].Type != "choice" {
		t.Fatalf("luna was asked %q", got.body.Questions[0].Type)
	}
	if out.Kind != rpc.DecideScore || out.Score == nil || math.Abs(*out.Score-0.03) > 1e-9 || !out.Rows[0].Chosen {
		t.Fatalf("out = %+v score %v", out, *out.Score)
	}
}

func TestAScoreNeedsAScale(t *testing.T) {
	e, _ := engineReplying(t, liveScore)
	for _, req := range []rpc.DecideRequest{
		{Question: "is it?", Score: true},
		{Question: "is it?", Choices: []string{"yes", "no"}, Score: true},
	} {
		if _, err := e.Decide(context.Background(), Source{Defaults: jevDefaults}, req); err == nil {
			t.Fatalf("%+v was sent as a score", req)
		}
	}
}
