package cli

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/form/replica"
	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
	"github.com/jack-work/jkrpc"
)

func TestShownFormEditParses(t *testing.T) {
	base := form.FromMap(formKeys("a", `1`, "b.c", `"x"`))
	cases := []struct {
		line   string
		ok     bool
		id     string
		after  string
		absent string
	}{
		{line: `set a 2`, ok: true, after: `{"a":2,"b":{"c":"x"}}`},
		{line: `form set b.c 'hello world'`, ok: true, after: `{"a":1,"b":{"c":"hello world"}}`},
		{line: `state set --id x1 n '{"k":[1]}'`, ok: true, id: "x1", after: `{"a":1,"b":{"c":"x"},"n":{"k":[1]}}`},
		{line: `form delete a,b.c`, ok: true, after: `{"b":{}}`},
		{line: `unset --id=x2 a`, ok: true, id: "x2", after: `{"b":{"c":"x"}}`},
		{line: `form set a=1,b=2`, ok: false},
		{line: `set -j a 1`, ok: false},
		{line: `set a[0] 1`, ok: false},
		{line: `form ls`, ok: false},
		{line: `set a`, ok: false},
	}
	for _, tc := range cases {
		p, id, ok := shownFormEdit(tokenize(tc.line), base)
		if ok != tc.ok {
			t.Fatalf("%s: routed=%v, want %v", tc.line, ok, tc.ok)
		}
		if !ok {
			continue
		}
		if id != tc.id {
			t.Fatalf("%s: id %q, want %q", tc.line, id, tc.id)
		}
		got, _ := base.Apply(p).MarshalJSON()
		if string(got) != tc.after {
			t.Fatalf("%s: form becomes %s, want %s", tc.line, got, tc.after)
		}
	}
}

// c fills the : line with a command that, run as typed, sets the same value.
func TestChangeLineRoundTrips(t *testing.T) {
	values := []string{`"plain"`, `"it's quoted \"twice\""`, `[1, {"b": "x y"}]`, `42`, `"back\\slash"`, `""`, `"tab\there"`}
	for _, val := range values {
		src := &fakeFormSource{snap: form.FromMap(formKeys("k.v", val))}
		v := &formView{src: src, open: map[string]bool{"k": true}, aria: "host"}
		v.openRev++
		v.Items(80)
		line, note := v.changeLine("k.v", "host")
		if line == "" {
			t.Fatalf("%s: no line (%s)", val, note)
		}
		if strings.Contains(line, "--id") {
			t.Fatalf("the subject's own form needs no --id: %s", line)
		}
		p, _, ok := shownFormEdit(tokenize(line), src.snap)
		if !ok {
			t.Fatalf("%s: the filled line does not route: %s", val, line)
		}
		if !p.IsIdentity() {
			t.Fatalf("%s: running the line unchanged would change the form: %s", val, line)
		}
	}
	src := &fakeFormSource{snap: form.FromMap(formKeys("k", `1`))}
	v := &formView{src: src, open: map[string]bool{}, aria: "other"}
	v.Items(80)
	if line, _ := v.changeLine("k", "host"); !strings.Contains(line, "--id other") {
		t.Fatalf("another aria's form must be named: %s", line)
	}
	ro := &formView{mirror: &formMirror{}, open: map[string]bool{}}
	if line, note := ro.changeLine("k", "host"); line != "" || note == "" {
		t.Fatalf("a read-only form offered a line %q (%q)", line, note)
	}
}

func TestFormViewEditsThroughItsSource(t *testing.T) {
	src := &fakeFormSource{snap: form.FromMap(formKeys("a", `1`))}
	v := &formView{src: src, open: map[string]bool{}}
	p, _, _ := shownFormEdit(tokenize("set a 5"), src.snap)
	if err := v.Edit(p); err != nil {
		t.Fatal(err)
	}
	if got := formRowText(v.Items(80), "a"); !strings.Contains(got, "5") {
		t.Fatalf("the edit is not on screen: %q", got)
	}
	m := &formMirror{}
	m.reset(form.FromMap(formKeys("a", `1`)), 1, 1)
	if err := (&formView{mirror: m}).Edit(p); err == nil {
		t.Fatal("a bare mirror accepted an edit it cannot send")
	}
}

// A view that stopped tracking (a daemon of another form schema) refuses an
// edit with the reason it stopped, rather than send it to be folded by rules
// this client does not share.
func TestAStoppedFormViewRefusesEdits(t *testing.T) {
	src := &fakeFormSource{snap: form.FromMap(formKeys("a", `1`))}
	v := &formView{src: src, open: map[string]bool{}}
	v.stop("this daemon speaks form schema 3, this client speaks 2: not tracking")
	p, _, _ := shownFormEdit(tokenize("set a 5"), src.snap)
	err := v.Edit(p)
	if err == nil || !strings.Contains(err.Error(), "not tracking") {
		t.Fatalf("a stopped view took an edit: %v", err)
	}
	if got := formRowText(v.Items(80), "a"); strings.Contains(got, "5") {
		t.Fatalf("the refused edit reached the source: %q", got)
	}
}

// c in a form pit fills the : line through the real key path; any other pit
// keeps the old meaning of c.
func TestChangeKeyFillsTheCommandLine(t *testing.T) {
	tr := newTranscript(io.Discard, 100, 30, ldrender.NodeText{}, aria.NewClient(), "aria1234", time.Unix(0, 0))
	tr.enter()
	src := &fakeFormSource{snap: form.FromMap(formKeys("mantra", `"old words"`, "n", `1`))}
	v := &formView{src: src, open: map[string]bool{}, aria: "aria1234"}
	v.change = func() { pitChange(tr) }
	tr.showLivePit("form show", v, false)
	tr.pit.lines(100, 20)
	tr.key('c')
	if tr.box != boxCommand {
		t.Fatalf("c did not open the : line (box %v)", tr.box)
	}
	if got := tr.cmdline.String(); got != `form set mantra '"old words"'` {
		t.Fatalf("the line reads %q", got)
	}
	p, id, ok := shownFormEdit(tokenize(tr.cmdline.String()), src.snap)
	if !ok || id != "" || !p.IsIdentity() {
		t.Fatalf("the filled line does not round-trip: ok=%v id=%q", ok, id)
	}
}

// fakeFormServer is the smallest honest server for a pit's replica: one
// writer, consecutive versions, deltas that name their origin, and refusal
// of any write to a key named "locked".
type fakeFormServer struct {
	mu      sync.Mutex
	snap    form.Snapshot
	version uint64
	last    map[string]uint64
	hold    chan struct{}
	rep     *replica.Replica
}

func (s *fakeFormServer) Load(_ context.Context, client string) (*rpc.FormResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &rpc.FormResponse{Snapshot: s.snap, Version: s.version, Incarnation: 1, LastSeq: s.last[client], Origin: "o:" + client}, nil
}

func (s *fakeFormServer) Set(_ context.Context, req rpc.SetRequest) (*rpc.SetResponse, error) {
	if s.hold != nil {
		<-s.hold
	}
	s.mu.Lock()
	for _, e := range req.Patch.Entries() {
		if strings.HasPrefix(e.Key, "locked") {
			s.last[req.Client] = req.Seq
			s.mu.Unlock()
			return nil, &jkrpc.Error{Code: -32000, Message: "locked is locked"}
		}
	}
	s.snap = s.snap.Apply(req.Patch)
	s.version++
	s.last[req.Client] = req.Seq
	d := rpc.FormDelta{Schema: rpc.FormDeltaSchema, Incarnation: 1, Version: s.version, Patch: req.Patch, Origin: "o:" + req.Client, Seq: req.Seq}
	v := s.version
	s.mu.Unlock()
	s.rep.Delta(d)
	return &rpc.SetResponse{OK: true, Outcome: rpc.OutcomeApplied, Version: v}, nil
}

func pitWithReplica(t *testing.T, srv *fakeFormServer) (*formView, chan struct{}) {
	t.Helper()
	changed := make(chan struct{}, 64)
	v := &formView{open: map[string]bool{}, aria: "host"}
	r := replica.New("pit-test", srv, replica.Options{
		OnChange:  func(_, _ form.Snapshot) { changed <- struct{}{} },
		OnRefused: func(_ uint64, err error) { v.refuse(err); changed <- struct{}{} },
	})
	srv.rep = r
	t.Cleanup(r.Close)
	<-r.Ready()
	v.src = replicaSource{r}
	return v, changed
}

func waitFor(t *testing.T, changed chan struct{}, what string, ok func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !ok() {
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// An edit is on screen before the server answers, marked pending, and the
// mark goes when the server's own delta settles it.
func TestPitEditIsOptimisticAndMarkedUntilSettled(t *testing.T) {
	srv := &fakeFormServer{snap: form.FromMap(formKeys("a", `1`, "g.x", `"old"`)), version: 3, last: map[string]uint64{}, hold: make(chan struct{})}
	v, changed := pitWithReplica(t, srv)
	v.open["g"] = true
	v.openRev++
	p, _, _ := shownFormEdit(tokenize(`set g.x '"new"'`), v.source().Shown())
	if err := v.Edit(p); err != nil {
		t.Fatal(err)
	}
	waitFor(t, changed, "the optimistic value", func() bool {
		return strings.Contains(formRowText(v.Items(80), "g.x"), "new")
	})
	rows := v.Items(80)
	for _, id := range []string{"g", "g.x"} {
		if r := formRowByID(rows, id); r.note != "pending" {
			t.Fatalf("row %s is not marked pending while the server holds the edit: %+v", id, r.note)
		}
	}
	if r := formRowByID(rows, "a"); r.note != "" {
		t.Fatalf("an untouched row is marked: %q", r.note)
	}
	close(srv.hold)
	waitFor(t, changed, "the edit to settle", func() bool { return formRowByID(v.Items(80), "g.x").note == "" })
	if got := formRowText(v.Items(80), "g.x"); !strings.Contains(got, "new") {
		t.Fatalf("the settled value is %q", got)
	}
}

// A refused edit leaves the screen and says so; the next edit clears the word.
func TestPitRefusedEditIsTakenBack(t *testing.T) {
	srv := &fakeFormServer{snap: form.FromMap(formKeys("a", `1`)), version: 1, last: map[string]uint64{}}
	v, changed := pitWithReplica(t, srv)
	p, _, _ := shownFormEdit(tokenize(`set locked 1`), v.source().Shown())
	v.Edit(p)
	waitFor(t, changed, "the refusal", func() bool {
		rows := v.Items(80)
		return len(rows) > 0 && strings.Contains(rows[0].text, "refused") && formRowText(rows, "locked") == ""
	})
	p, _, _ = shownFormEdit(tokenize(`set a 2`), v.source().Shown())
	v.Edit(p)
	waitFor(t, changed, "the next edit", func() bool {
		rows := v.Items(80)
		return strings.Contains(formRowText(rows, "a"), "2") && !strings.Contains(rows[0].text, "refused")
	})
}

func formRowByID(rows []pitRow, id string) pitRow {
	for _, r := range rows {
		if r.id == id {
			return r
		}
	}
	return pitRow{}
}

// A daemon without form.seq would drop a replica's client and seq, so a resent
// edit could apply twice: against it the pit opens on the read-only mirror.
// Asking must not register a need, or the next handshake with that daemon
// would end the process.
func TestThePitIsReadOnlyAgainstADaemonWithoutFormSeq(t *testing.T) {
	resetDaemonSeen(t)
	if !formPitEditable("") {
		t.Fatal("no daemon seen yet: nothing says the pit cannot edit")
	}
	old := daemonIdentity{build: "old", release: "0.52.4", protocol: 1, caps: []rpc.Capability{rpc.CapQuaImages}}
	sawDaemon("the daemon", old, "new")
	if formPitEditable("") {
		t.Fatal("the pit would edit through a replica against a daemon that drops seqs")
	}
	if formPitEditable("runtime") {
		t.Fatal("an intrinsic form is never editable")
	}
	daemonSeen.Lock()
	pending := len(daemonSeen.needs)
	daemonSeen.Unlock()
	if pending != 0 {
		t.Fatalf("asking about form.seq registered %d need(s)", pending)
	}
	sawDaemon("the daemon", daemonIdentity{build: "new2", release: "0.53.0", protocol: 1, caps: []rpc.Capability{rpc.CapQuaImages, rpc.CapFormSeq}}, "new")
	if !formPitEditable("") {
		t.Fatal("a daemon reporting form.seq gets a replica")
	}
	m := &formMirror{}
	m.reset(form.FromMap(formKeys("a", `1`)), 1, 1)
	p, _, _ := shownFormEdit(tokenize("set a 5"), m.snap)
	if err := (&formView{mirror: m}).Edit(p); err == nil {
		t.Fatal("the read-only mirror took an edit")
	}
}

// The pit's replica is bound to the daemon it was opened against. When that
// link dies the pit stops and sends nothing more; the next daemon is reached
// only by reopening, which asks the new handshake for form.seq first. So a
// pit can never hand a seq'd edit to a daemon that would drop the seq.
func TestAPitWhoseDaemonIsSwappedStopsAndReopensReadOnly(t *testing.T) {
	resetDaemonSeen(t)
	sawDaemon("the daemon", daemonIdentity{build: "new2", release: "0.53.0", protocol: 1, caps: []rpc.Capability{rpc.CapQuaImages, rpc.CapFormSeq}}, "new")
	if !formPitEditable("") {
		t.Fatal("a daemon reporting form.seq gets a replica")
	}
	srv := &fakeFormServer{snap: form.FromMap(formKeys("a", `1`)), version: 1, last: map[string]uint64{}, hold: make(chan struct{})}
	v, changed := pitWithReplica(t, srv)
	link, closing := make(chan struct{}), make(chan struct{})
	r := v.src.(replicaSource).Replica
	stopped := make(chan struct{})
	go func() {
		stopPitWhenLinkDies(link, closing, v, r)
		close(stopped)
	}()
	p, _, _ := shownFormEdit(tokenize("set a 5"), form.FromMap(formKeys("a", `1`)))
	if err := v.Edit(p); err != nil {
		t.Fatal(err)
	}
	waitFor(t, changed, "the edit on screen", func() bool { return strings.Contains(formRowText(v.Items(80), "a"), "5") })

	close(link)
	<-stopped
	close(srv.hold)
	if err := v.Edit(p); err == nil || !strings.Contains(err.Error(), "connection closed") {
		t.Fatalf("a pit whose link died took an edit: %v", err)
	}
	if got := formRowText(v.Items(80), ""); !strings.Contains(got, "unanswered") {
		t.Fatalf("the pit does not say an edit went unanswered: %q", got)
	}

	sawDaemon("the daemon", daemonIdentity{build: "old", release: "0.52.4", protocol: 1, caps: []rpc.Capability{rpc.CapQuaImages}}, "new")
	if formPitEditable("") {
		t.Fatal("reopened against a daemon without form.seq, the pit would edit through a replica")
	}
}

// An edit the server never answered must not vanish with the process: when the
// pager exits, every pit with unanswered edits says so on stderr.
func TestUnansweredPitEditsAreReportedAtExit(t *testing.T) {
	srv := &fakeFormServer{snap: form.FromMap(formKeys("a", `1`)), version: 1, last: map[string]uint64{}, hold: make(chan struct{})}
	v, changed := pitWithReplica(t, srv)
	r := v.src.(replicaSource).Replica
	notePit("@form-a", r.Pending)
	settled := &fakeFormServer{snap: form.FromMap(formKeys("b", `1`)), version: 1, last: map[string]uint64{}}
	w, wchanged := pitWithReplica(t, settled)
	notePit("@form-b", w.src.(replicaSource).Replica.Pending)

	p, _, _ := shownFormEdit(tokenize("set a 5"), form.FromMap(formKeys("a", `1`)))
	if err := v.Edit(p); err != nil {
		t.Fatal(err)
	}
	waitFor(t, changed, "the edit on screen", func() bool { return strings.Contains(formRowText(v.Items(80), "a"), "5") })
	q, _, _ := shownFormEdit(tokenize("set b 6"), form.FromMap(formKeys("b", `1`)))
	if err := w.Edit(q); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wchanged, "the other pit's edit settled", func() bool { return w.src.(replicaSource).Replica.Pending() == 0 })

	link, closing := make(chan struct{}), make(chan struct{})
	stopped := make(chan struct{})
	go func() { stopPitWhenLinkDies(link, closing, v, r); close(stopped) }()
	close(link)
	<-stopped
	defer close(srv.hold)

	var out strings.Builder
	reportUnansweredPitEdits(&out)
	if got, want := out.String(), "1 form edit(s) to @form-a were not confirmed\n"; got != want {
		t.Fatalf("exit report = %q, want %q", got, want)
	}
	out.Reset()
	reportUnansweredPitEdits(&out)
	if out.Len() != 0 {
		t.Fatalf("reported twice: %q", out.String())
	}
}
