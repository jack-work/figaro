package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

func studiedSnap(ids ...string) form.Snapshot {
	raw, _ := json.Marshal(ids)
	return form.FromMap(formKeys(form.StudiesKey, string(raw)))
}

type studyOpens struct {
	opened []string
	closed []string
	forms  map[string]map[string]json.RawMessage
}

func (o *studyOpens) open(addr string) (*formView, actOn, func(), <-chan struct{}, error) {
	o.opened = append(o.opened, addr)
	snap := form.FromMap(o.forms[addr])
	v := &formView{src: &fakeFormSource{snap: snap}, open: map[string]bool{}}
	return v, nil, func() { o.closed = append(o.closed, addr) }, nil, nil
}

func TestStudyTabsAreNumberedOneToNineThenZeroAndStopAtTen(t *testing.T) {
	var ids []string
	for i := 0; i < 12; i++ {
		ids = append(ids, fmt.Sprintf("@%032x", i))
	}
	var s studiesPit
	if _, refusal := s.choose("a", studiedSnap(ids...), -1); refusal != "" {
		t.Fatal(refusal)
	}
	if len(s.forms) != 10 || s.more != 2 {
		t.Fatalf("forms %d more %d", len(s.forms), s.more)
	}
	strip := s.strip()
	if !strings.HasPrefix(strip, "+2 more  ") {
		t.Fatalf("the overflow is said first, where a narrow pane keeps it: %q", strip)
	}
	for _, want := range []string{"[1 ", " 9 ", " 0 "} {
		if !strings.Contains(strip, want) {
			t.Fatalf("strip %q lacks %q", strip, want)
		}
	}
	if studyKeyIndex('0') != 9 || studyKeyIndex('1') != 0 || studyKeyIndex('9') != 8 {
		t.Fatal("digit keys do not map 1..9 then 0 onto tabs")
	}
}

func TestStudyTabsLoadOnFirstVisitOnly(t *testing.T) {
	o := &studyOpens{forms: map[string]map[string]json.RawMessage{
		"@a": formKeys("brief", `"x"`),
		"@b": formKeys("name", `"plain"`),
	}}
	var s studiesPit
	snap := studiedSnap("@a", "@b")
	idx, _ := s.choose("fig", snap, 1)
	if len(o.opened) != 0 {
		t.Fatal("choosing a tab dialled it")
	}
	first, err := s.view(idx, o.open)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := s.view(idx, o.open)
	if first != again || len(o.opened) != 1 || o.opened[0] != "@b" {
		t.Fatalf("opened %q, same view %v", o.opened, first == again)
	}
	if !strings.Contains(s.strip(), "[2 plain]") || !strings.Contains(s.strip(), "1 @a") {
		t.Fatalf("strip %q: the visited tab is named, the other is not dialled", s.strip())
	}
}

func TestStudyTabNameIsRoleNameThenNameThenShortID(t *testing.T) {
	if got := studyFormName(form.FromMap(formKeys("role-name", `"Reviewer"`, "name", `"x"`))); got != "Reviewer" {
		t.Fatalf("role-name: %q", got)
	}
	if got := studyFormName(form.FromMap(formKeys("name", `"plain"`))); got != "plain" {
		t.Fatalf("name: %q", got)
	}
	if got := studyFormName(form.FromMap(formKeys("brief", `"x"`))); got != "" {
		t.Fatalf("neither: %q", got)
	}
}

func TestStudyDefaultTabIsTheLastShownOtherwiseTheFirst(t *testing.T) {
	var s studiesPit
	snap := studiedSnap("@a", "@b", "@c")
	if idx, _ := s.choose("fig", snap, -1); idx != 0 {
		t.Fatalf("first use opened tab %d", idx)
	}
	s.choose("fig", snap, 2)
	if idx, _ := s.choose("fig", snap, -1); idx != 2 {
		t.Fatalf("default is tab %d, want the last shown (2)", idx)
	}
	if idx, _ := s.choose("other", snap, -1); idx != 0 {
		t.Fatalf("another figaro inherited tab %d", idx)
	}
}

func TestNoStudiesAndNoSuchTabAreSaidNotOpened(t *testing.T) {
	var s studiesPit
	if _, refusal := s.choose("fig", form.FromMap(formKeys()), -1); !strings.Contains(refusal, "no studies") {
		t.Fatalf("empty: %q", refusal)
	}
	if _, refusal := s.choose("fig", studiedSnap("@a"), 3); !strings.Contains(refusal, "no study 4") {
		t.Fatalf("out of range: %q", refusal)
	}
}

func TestStudyListFallsBackToTheLegacySystemKey(t *testing.T) {
	snap := form.FromMap(formKeys("system.studies", `["@old"]`))
	if got := studiedForms(snap); len(got) != 1 || got[0] != "@old" {
		t.Fatalf("%q", got)
	}
}

func TestDroppedTabsAreClosedAndAStoppedTabIsRedialled(t *testing.T) {
	o := &studyOpens{forms: map[string]map[string]json.RawMessage{"@a": formKeys("name", `"a"`)}}
	var s studiesPit
	idx, _ := s.choose("fig", studiedSnap("@a"), 0)
	v, _ := s.view(idx, o.open)
	v.stop("link lost")
	v2, _ := s.view(idx, o.open)
	if v2 == v || len(o.opened) != 2 || len(o.closed) != 1 {
		t.Fatalf("opened %q closed %q", o.opened, o.closed)
	}
	s.choose("someone else", studiedSnap("@a"), 0)
	if len(o.closed) != 2 {
		t.Fatalf("attending another figaro left the tab open: %q", o.closed)
	}
}

func TestTheStripIsTheTitleOfThePitShowingATab(t *testing.T) {
	o := &studyOpens{forms: map[string]map[string]json.RawMessage{"@a": formKeys("name", `"alpha"`)}}
	var s studiesPit
	idx, _ := s.choose("fig", studiedSnap("@a", "@b"), 0)
	v, _ := s.view(idx, o.open)
	v.strip = s.strip
	tr := newTranscript(io.Discard, 100, 30, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	tr.enter()
	tr.showLivePit(studiesPitName, v, false)
	got := tr.pit.lines(100, 10)
	if !strings.Contains(stripANSI(got[0]), "[1 alpha]") || !strings.Contains(stripANSI(got[0]), "2 @b") {
		t.Fatalf("first row %q", got[0])
	}
}
