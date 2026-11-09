package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

var indexHint = &rpc.FormView{
	Rows:  []string{"*"},
	Mount: "form",
	Mark:  &rpc.ViewMark{Key: "self", Equals: "true"},
}

var thingsHint = &rpc.FormView{
	Rows:    []string{"items.*"},
	Open:    []string{"items"},
	Sort:    "-n",
	Label:   "name",
	RowAria: "id",
	Actions: []rpc.ViewAction{{Key: "x", Label: "drop", Method: "things.drop"}},
}

type fakeMounts struct {
	mu     sync.Mutex
	links  map[string]chan struct{}
	forms  map[string]*fakeFormSource
	errs   map[string]error
	opened []string
	closed []string
	sent   []string
	kick   chan struct{}
	hint   *rpc.FormView
	plain  map[string]bool
}

func newFakeMounts() *fakeMounts {
	return &fakeMounts{forms: map[string]*fakeFormSource{}, errs: map[string]error{}, kick: make(chan struct{}, 16)}
}

func (f *fakeMounts) open(addr string) (*formView, actOn, func(), <-chan struct{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, addr)
	defer func() {
		select {
		case f.kick <- struct{}{}:
		default:
		}
	}()
	if err := f.errs[addr]; err != nil {
		return nil, nil, nil, nil, err
	}
	v := &formView{src: f.forms[addr], open: map[string]bool{}}
	hint := thingsHint
	if f.hint != nil {
		hint = f.hint
	}
	if !f.plain[addr] {
		v.setView(hint)
	}
	send := func(_ context.Context, method string, _ json.RawMessage, target string) error {
		f.mu.Lock()
		f.sent = append(f.sent, addr+" "+method+" "+target)
		f.mu.Unlock()
		return nil
	}
	link := make(chan struct{})
	if f.links == nil {
		f.links = map[string]chan struct{}{}
	}
	f.links[addr] = link
	return v, send, func() {
		f.mu.Lock()
		f.closed = append(f.closed, addr)
		f.mu.Unlock()
	}, link, nil
}

func (f *fakeMounts) waitOpened(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		f.mu.Lock()
		got := len(f.opened)
		f.mu.Unlock()
		if got >= n {
			time.Sleep(10 * time.Millisecond)
			return
		}
		select {
		case <-f.kick:
		case <-deadline:
			t.Fatalf("%d mounts opened, want %d", got, n)
		}
	}
}

func (f *fakeMounts) waitSent(t *testing.T, n int) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		got := strings.Join(f.sent, ";")
		count := len(f.sent)
		f.mu.Unlock()
		if count >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func things(kv ...string) *fakeFormSource {
	return &fakeFormSource{snap: form.FromMap(formKeys(kv...))}
}

func indexView(f *fakeMounts) *formView {
	src := &fakeFormSource{snap: form.FromMap(formKeys(
		"here.self", `true`, "here.form", `"@things"`,
		"far.self", `false`, "far.form", `"far/@things"`,
	))}
	v := &formView{src: src, open: map[string]bool{}, opener: f.open}
	v.setView(indexHint)
	return v
}

func rowsByID(rows []pitRow) map[string]pitRow {
	out := map[string]pitRow{}
	for _, r := range rows {
		if r.id != "" {
			out[r.id] = r
		}
	}
	return out
}

// A form whose hint names a mount draws the form each row points at under
// that row, with the mounted form's own hint: its rows, order and labels.
// The pointer leaf itself is not drawn, and the row's other leaves stay folded.
// Nothing about the mount is @arias.
func TestAMountedFormDrawsUnderItsPointer(t *testing.T) {
	f := newFakeMounts()
	f.forms["@things"] = things("items.a.n", `1`, "items.a.name", `"alpha"`, "items.b.n", `2`, "items.b.name", `"beta"`)
	v := indexView(f)
	v.Items(100)
	if len(f.opened) != 0 {
		t.Fatalf("a closed row opened its mount: %v", f.opened)
	}
	v.Activate("here")
	v.Items(100)
	f.waitOpened(t, 1)
	rows := v.Items(100)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.id)
	}
	got := strings.Join(ids, ",")
	want := "far,here,here\x1fitems,here\x1fitems.b,here\x1fitems.a"
	if got != want {
		t.Fatalf("rows = %q, want %q", got, want)
	}
	byID := rowsByID(rows)
	if s := byID["here\x1fitems.b"].str(); !strings.HasPrefix(s, "   ") || !strings.Contains(s, "beta") {
		t.Errorf("mounted row %q is not drawn under its pointer with its own label", s)
	}
	if byID["here"].note != "●" {
		t.Errorf("the self row is not marked")
	}
}

// Each mount is its own form: a change in one moves only its rows, and a
// pointer the index no longer holds closes its mount.
func TestMountsUpdateIndependentlyAndCloseWithTheirPointer(t *testing.T) {
	f := newFakeMounts()
	f.forms["@things"] = things("items.a.name", `"alpha"`)
	f.forms["far/@things"] = things("items.z.name", `"zulu"`)
	v := indexView(f)
	v.Items(100)
	v.Activate("here")
	v.Activate("far")
	v.Items(100)
	f.waitOpened(t, 2)
	before := rowsByID(v.Items(100))
	f.forms["far/@things"].snap = form.FromMap(formKeys("items.z.name", `"zulu"`, "items.y.name", `"yankee"`))
	after := rowsByID(v.Items(100))
	if _, ok := after["far\x1fitems.y"]; !ok {
		t.Error("a change in far's form did not reach the pit")
	}
	if before["here\x1fitems.a"].str() != after["here\x1fitems.a"].str() {
		t.Error("a change in far's form moved here's rows")
	}
	v.src.(*fakeFormSource).snap = form.FromMap(formKeys("here.self", `true`, "here.form", `"@things"`))
	rows := rowsByID(v.Items(100))
	if _, ok := rows["far\x1fitems.z"]; ok {
		t.Error("a pointer the index dropped still draws its mount")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		closed := strings.Join(f.closed, ",")
		f.mu.Unlock()
		if closed == "far/@things" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("closed = %q, want far's mount closed", closed)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Items runs several times per frame. With a mount open it rebuilt the whole
// tree every call, so held j in l spent 12 ms a key on 400 figaros and fell
// behind the key repeat. Nothing changed means the same rows, not a rebuild;
// a change inside the mount still rebuilds.
func TestAnOpenMountDoesNotRebuildAnUnchangedPit(t *testing.T) {
	f := newFakeMounts()
	f.forms["@things"] = things("items.a.name", `"alpha"`)
	f.forms["far/@things"] = things("items.z.name", `"zulu"`)
	v := indexView(f)
	v.Items(100)
	v.Activate("far")
	v.Items(100)
	f.waitOpened(t, 1)
	v.Items(100)
	rev := v.Revision()
	for range 5 {
		v.Items(100)
	}
	if got := v.Revision(); got != rev {
		t.Fatalf("five unchanged reads rebuilt the pit %d times", got-rev)
	}
	f.forms["far/@things"].snap = form.FromMap(formKeys("items.z.name", `"zulu"`, "items.y.name", `"yankee"`))
	if _, ok := rowsByID(v.Items(100))["far\x1fitems.y"]; !ok {
		t.Fatal("a change in the mount did not reach the pit")
	}
	if v.Revision() == rev {
		t.Fatal("a change in the mount did not rebuild the pit")
	}
}

// A search re-read the mounted form at the width of the plain tree to check
// the cache, then at its own width to draw: two widths, so every read rebuilt
// the mount and the whole search, three or four times a keystroke.
func TestASearchDoesNotRebuildAnUnchangedMount(t *testing.T) {
	f := newFakeMounts()
	f.forms["@things"] = things("items.a.name", `"alpha"`)
	f.forms["far/@things"] = things("items.z.name", `"zulu alpha"`)
	v := indexView(f)
	v.Items(100)
	v.Activate("far")
	v.Items(100)
	f.waitOpened(t, 1)
	v.SetQuery("alpha")
	v.Items(100)
	rev := v.Revision()
	for range 5 {
		v.Items(100)
	}
	if got := v.Revision(); got != rev {
		t.Fatalf("five reads of an unchanged search rebuilt it %d times", got-rev)
	}
}

// A mount that cannot load says why on the row, and the rest of the pit is
// unaffected.
func TestAMountThatCannotLoadSaysWhy(t *testing.T) {
	f := newFakeMounts()
	f.forms["@things"] = things("items.a.name", `"alpha"`)
	f.errs["far/@things"] = errors.New("figaro troupe login kelliher")
	v := indexView(f)
	v.Items(100)
	v.Activate("far")
	v.Activate("here")
	v.Items(100)
	f.waitOpened(t, 2)
	rows := v.Items(100)
	var note pitRow
	for i, r := range rows {
		if r.id == "far" && i+1 < len(rows) {
			note = rows[i+1]
		}
	}
	if !strings.HasPrefix(note.str(), "└─ ") || !strings.Contains(note.str(), "figaro troupe login kelliher") || note.selectable() {
		t.Errorf("far's row says %q (selectable %v), want the login command", note.str(), note.selectable())
	}
	if _, ok := rowsByID(rows)["here\x1fitems.a"]; !ok {
		t.Error("a mount that failed hid the one that loaded")
	}
}

// Text from a mounted form is a peer's: its escapes never reach the
// terminal. An OSC 52 (clipboard) and a CSI clear in a value are drawn inert.
func TestAMountedFormCannotWriteEscapesToTheTerminal(t *testing.T) {
	f := newFakeMounts()
	evil := `"\u001b]52;c;aGk=\u0007owned\u001b[2J\u009b2J"`
	f.forms["far/@things"] = things("items.a.name", evil, "items.a.note", evil, "items.a.k\x1b]52;c;aGk=\x07\x1b[2J", `1`)
	v := indexView(f)
	v.Items(100)
	v.Activate("far")
	v.Items(100)
	f.waitOpened(t, 1)
	v.Items(100)
	v.Activate("far\x1fitems.a")
	v.Items(100)
	v.Activate("far\x1fitems.a.note")
	tr := shapedTranscript(t, v)
	tr.pit.focus("far\x1fitems.a")
	lines := tr.pit.lines(100, 30)
	if !strings.Contains(strings.Join(lines, "\n"), "owned") {
		t.Fatalf("the opened value is not on screen:\n%s", strings.Join(lines, "\n"))
	}
	for _, line := range lines {
		clean := stripSGR(line)
		if strings.ContainsAny(clean, "\x1b\x07\u009b") {
			t.Errorf("a mounted row put a control byte on the terminal: %q", clean)
		}
	}
}

func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == ';') {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func mountedPit(t *testing.T, f *fakeMounts, open ...string) (*interactiveInput, *livelogTurn, *formView) {
	t.Helper()
	var out countingWriter
	in, lt := navInput(t, &out, true)
	v := indexView(f)
	v.act = func(a rpc.ViewAction) {
		pitAct(in, a, func(context.Context, string, json.RawMessage, string) error {
			t.Errorf("an action in a mount went to the local daemon")
			return nil
		})
	}
	in.mu.Lock()
	lt.tr.showLivePit("troupe", v, false)
	lt.tr.pit.lines(100, 30)
	for _, row := range open {
		v.Activate(row)
		v.Items(100)
	}
	in.mu.Unlock()
	f.waitOpened(t, len(open))
	in.mu.Lock()
	lt.tr.pit.lines(100, 30)
	in.mu.Unlock()
	return in, lt, v
}

func focusLocked(in *interactiveInput, lt *livelogTurn, id string) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	lt.tr.pit.lines(100, 30)
	return lt.tr.pit.focus(id)
}

// A mounted row's action goes over that mount's own connection, against the
// id qualified with the mount's node; a row whose id already names a node
// is refused, and nothing reaches the local daemon.
func TestAMountedActionStaysInItsMount(t *testing.T) {
	f := newFakeMounts()
	f.forms["far/@things"] = things("items.a.id", `"abc"`, "items.a.n", `1`, "items.b.id", `"othernode/def"`, "items.b.n", `2`)
	in, lt, _ := mountedPit(t, f, "far")

	if !focusLocked(in, lt, "far\x1fitems.a") {
		t.Fatal("no mounted row items.a")
	}
	feed(t, in, "x")
	sent := f.waitSent(t, 1)
	if !strings.Contains(sent, "far/@things things.drop far/abc") {
		t.Errorf("sent %q, want things.drop on far/abc over far's connection", sent)
	}

	if !focusLocked(in, lt, "far\x1fitems.b") {
		t.Fatal("no mounted row items.b")
	}
	feed(t, in, "x")
	time.Sleep(100 * time.Millisecond)
	f.mu.Lock()
	n := len(f.sent)
	f.mu.Unlock()
	if n != 1 {
		t.Errorf("a row naming another node sent its action: %v", f.sent)
	}
}

// l on a peer's row opens its mount, typed as a key; the mount is not
// dialled before.
func TestLOnAPeerRowOpensItsMount(t *testing.T) {
	f := newFakeMounts()
	f.forms["far/@things"] = things("items.a.n", `1`)
	in, lt, v := mountedPit(t, f)
	if !focusLocked(in, lt, "far") {
		t.Fatal("no far row")
	}
	if len(f.opened) != 0 {
		t.Fatalf("a peer was dialled before its row opened: %v", f.opened)
	}
	feed(t, in, "l")
	in.mu.Lock()
	lt.tr.pit.lines(100, 30)
	in.mu.Unlock()
	f.waitOpened(t, 1)
	if _, ok := rowsByID(v.Items(100))["far\x1fitems"]; !ok || f.opened[0] != "far/@things" {
		t.Errorf("l did not mount far: opened %v", f.opened)
	}
}

// A lapse is followed again inside the mounted view (with a fresh token: the
// endpoint resolves its bearer on every dial). When a mount's link ends for
// any other reason, the pit dials the mount again and keeps drawing.
func TestAMountWhoseLinkEndsDialsAgain(t *testing.T) {
	f := newFakeMounts()
	f.forms["far/@things"] = things("items.a.n", `1`)
	v := indexView(f)
	v.Items(100)
	v.Activate("far")
	v.Items(100)
	f.waitOpened(t, 1)
	f.mu.Lock()
	close(f.links["far/@things"])
	f.mu.Unlock()
	f.waitOpened(t, 2)
	if _, ok := rowsByID(v.Items(100))["far\x1fitems"]; !ok {
		t.Error("the redialled mount is not drawn")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Join(f.closed, ",") != "far/@things" {
		t.Errorf("closed %v, want the ended connection closed once", f.closed)
	}
}

// An open pointer row draws its mount as its last child, under the same
// guides as the rest of the tree. Its own scalar leaves stay folded until l
// opens them; closing the row folds them again.
func TestAnOpenPointerRowFoldsItsOwnLeavesAndGuidesItsMount(t *testing.T) {
	f := newFakeMounts()
	f.forms["@things"] = things("items.a.n", `1`, "items.a.name", `"alpha"`, "items.b.n", `2`, "items.b.name", `"beta"`)
	src := &fakeFormSource{snap: form.FromMap(formKeys(
		"here.self", `true`, "here.version", `"0.54.1"`, "here.form", `"@things"`,
	))}
	v := &formView{src: src, open: map[string]bool{}, opener: f.open}
	v.setView(indexHint)
	v.Items(100)
	v.Activate("here")
	v.Items(100)
	f.waitOpened(t, 1)

	drawn := func() []string {
		var out []string
		for _, r := range v.Items(100) {
			if r.id != "" {
				out = append(out, r.id+"|"+r.str())
			}
		}
		return out
	}
	want := []string{
		"here|here (3)",
		"here\x1fitems|└─ items (2)",
		"here\x1fitems.b|   ├─ b (2)  \"beta\"",
		"here\x1fitems.a|   └─ a (2)  \"alpha\"",
	}
	check := func(stage string, want []string) {
		t.Helper()
		got := drawn()
		if len(got) != len(want) {
			t.Fatalf("%s: rows\n%s\nwant\n%s", stage, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		for i := range want {
			if !strings.HasPrefix(got[i], want[i]) {
				t.Fatalf("%s: rows\n%s\nwant\n%s", stage, strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		}
	}
	check("open", want)

	v.Expand("here")
	check("l on the open row", []string{
		"here|here (3)",
		"here.self|├─ self:",
		"here.version|├─ version:",
		"here\x1fitems|└─ items (2)",
		"here\x1fitems.b|   ├─ b (2)  \"beta\"",
		"here\x1fitems.a|   └─ a (2)  \"alpha\"",
	})

	v.Activate("here")
	v.Activate("here")
	check("closed and opened again", want)
}

// A search in the troupe's view indexed only the index's own keys, so Gluck
// could find "form" and nothing else: never an aria's mantra or id inside a
// mounted @arias. A search reaches every loaded mount: the hit is drawn under
// its pointer with its letters marked, ^N counts it, Enter lands on the row,
// and Enter again attends the aria it names.
func TestASearchReachesIntoLoadedMounts(t *testing.T) {
	const id = "09e4b786557992e7a84fbff7606463f7"
	f := newFakeMounts()
	f.forms["@things"] = things(
		"items.a.n", `1`, "items.a.name", `"restart the bindings"`, "items.a.id", `"`+id+`"`,
		"items.b.n", `2`, "items.b.name", `"beta"`, "items.b.id", `"38f4563ac90d4206364a64c0a4f4c4fa"`,
	)
	hint := *thingsHint
	hint.Actions = []rpc.ViewAction{{Key: "Enter", Label: "attend", Local: rpc.ViewAttend}}
	f.hint = &hint
	in, lt, v := mountedPit(t, f, "here")
	var attended string
	lt.tr.attendAria = func(a string) { attended = a }
	for _, q := range []string{"bindings", id, id[:6], id[:8], id[:12]} {
		feed(t, in, "/"+q)
		in.mu.Lock()
		var marked []string
		for _, r := range lt.tr.pit.list().rows {
			if len(r.marks) > 0 {
				marked = append(marked, r.id)
			}
		}
		in.mu.Unlock()
		if len(marked) == 0 || !strings.HasPrefix(marked[0], "here\x1fitems.a") {
			t.Fatalf("/%s marked %q, want a row of items.a under its pointer", q, marked)
		}
		if _, n := v.QueryCount(); n != len(marked) {
			t.Errorf("/%s: the counter counts %d, the view draws %d", q, n, len(marked))
		}
		feed(t, in, "\r")
		in.mu.Lock()
		row, _ := lt.tr.pit.selected()
		in.mu.Unlock()
		if row.id != "here\x1fitems.a" {
			t.Fatalf("/%s Enter landed on %q, want here\\x1fitems.a", q, row.id)
		}
	}
	feed(t, in, "\r")
	if attended != id {
		t.Errorf("Enter on the found row attended %q, want %s", attended, id)
	}
}
