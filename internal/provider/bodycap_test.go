package provider

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func chatImageRow(t *testing.T, caption string, n int) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": caption},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + strings.Repeat("A", n)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func textRow(t *testing.T, text string) json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"role": "user", "content": text})
	return raw
}

func chatFrame(r RowSeq) BodyFunc {
	list, _ := CollectRows(r)
	frame := struct {
		Model    string            `json:"model"`
		Messages []json.RawMessage `json:"messages"`
	}{Model: "explabs/qwen3.8-max"}
	return func(w io.Writer) error { return WriteJSONWithRows(w, frame, "messages", list) }
}

func fit(t *testing.T, rows []json.RawMessage, limit int) ([]json.RawMessage, int, error) {
	t.Helper()
	got, err := FitRequest("rou2", SliceRows(rows, nil), limit, ChatImages, chatFrame)
	if err != nil {
		return nil, 0, err
	}
	out, _ := CollectRows(got)
	size, err := BodySize(chatFrame(got))
	return out, size, err
}

func imagesIn(rows []json.RawMessage) int {
	n := 0
	for _, r := range rows {
		n += strings.Count(string(r), `"type":"image_url"`)
	}
	return n
}

// gen5: a request of 8,776,742 bytes reached rou2, which cuts bodies at
// 8 MiB and answered "Request body must contain valid JSON".
func TestARequestOverTheLimitDropsTheOldestImagesFirst(t *testing.T) {
	rows := []json.RawMessage{
		chatImageRow(t, "first", 3000),
		textRow(t, "between"),
		chatImageRow(t, "second", 3000),
		chatImageRow(t, "third", 3000),
	}
	whole, _ := BodySize(chatFrame(SliceRows(rows, nil)))
	limit := whole - 4000

	got, size, err := fit(t, rows, limit)
	if err != nil {
		t.Fatal(err)
	}
	if size > limit {
		t.Fatalf("body %d is over the limit %d", size, limit)
	}
	if imagesIn(got) != 1 || !strings.Contains(string(got[3]), "image_url") {
		t.Errorf("want only the newest image kept, got %d images: %s", imagesIn(got), got)
	}
	for _, i := range []int{0, 2} {
		if !strings.Contains(string(got[i]), "[image elided: the request was over rou2's") {
			t.Errorf("row %d does not say what was elided: %s", i, got[i])
		}
	}
	if !strings.Contains(string(got[0]), `"text":"first"`) || string(got[1]) != string(rows[1]) {
		t.Errorf("text was touched: %s", got)
	}
}

func TestARequestUnderTheLimitIsUntouched(t *testing.T) {
	rows := []json.RawMessage{chatImageRow(t, "only", 100)}
	got, _, err := fit(t, rows, 1<<20)
	if err != nil || string(got[0]) != string(rows[0]) {
		t.Fatalf("got %s, %v", got, err)
	}
	got, _, err = fit(t, rows, 0)
	if err != nil || string(got[0]) != string(rows[0]) {
		t.Fatalf("limit 0 must mean no limit: %s, %v", got, err)
	}
}

func TestTextAloneOverTheLimitIsRequestTooLarge(t *testing.T) {
	rows := []json.RawMessage{chatImageRow(t, "pic", 500), textRow(t, strings.Repeat("words ", 2000))}
	_, _, err := fit(t, rows, 4000)
	var big *RequestTooLarge
	if !errors.As(err, &big) {
		t.Fatalf("got %v, want RequestTooLarge", err)
	}
	if big.Elided != 1 || !strings.Contains(err.Error(), "rou2: request too large") || !strings.Contains(err.Error(), MaxRequestBytesKey) {
		t.Errorf("err = %v", err)
	}
}

func TestEachDialectFindsItsImageParts(t *testing.T) {
	cases := map[string]struct {
		d   ImageDialect
		row string
	}{
		"messages":  {MessagesImages, `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`},
		"chat":      {ChatImages, `{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}`},
		"responses": {ResponsesImages, `{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}`},
	}
	for name, c := range cases {
		out, ok := elideOneImage(json.RawMessage(c.row), c.d, "gone")
		if !ok || strings.Contains(string(out), "AAAA") || !strings.Contains(string(out), `"text":"gone"`) {
			t.Errorf("%s: %s, %v", name, out, ok)
		}
		if _, again := elideOneImage(out, c.d, "gone"); again {
			t.Errorf("%s: a second image was found in %s", name, out)
		}
	}
}

func TestTheRequestLimitReadsItsKnob(t *testing.T) {
	set := func(v string) form.Snapshot {
		return form.FromMap(map[string]json.RawMessage{MaxRequestBytesKey: json.RawMessage(v)})
	}
	if MaxRequestBytes(form.Snapshot{}, 8323072) != 8323072 {
		t.Error("unset must take the provider's default")
	}
	if MaxRequestBytes(set(`4000000`), 8323072) != 4000000 || MaxRequestBytes(set(`"4000000"`), 8323072) != 4000000 {
		t.Error("a number or a numeric string sets it")
	}
	if MaxRequestBytes(set(`0`), 8323072) != 0 {
		t.Error("0 turns the limit off")
	}
}

func TestATextOnlyModelGetsEveryImageReplacedWithTheReason(t *testing.T) {
	rows := []json.RawMessage{chatImageRow(t, "one", 10), textRow(t, "plain"), chatImageRow(t, "two", 10)}
	in := SendInput{TextOnly: "model explabs/glm-5.3 takes text only"}
	got, err := ShapeRequest("rou2", SliceRows(rows, nil), in, 0, ChatImages, chatFrame)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := CollectRows(got)
	if imagesIn(out) != 0 || strings.Count(strings.Join(rawStrings(out), ""), "[image omitted: model explabs/glm-5.3 takes text only]") != 2 {
		t.Errorf("got %s", out)
	}
	if string(out[1]) != string(rows[1]) {
		t.Errorf("a text row was rewritten: %s", out[1])
	}
}

func rawStrings(rows []json.RawMessage) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = string(r)
	}
	return out
}
