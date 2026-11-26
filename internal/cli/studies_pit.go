package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/jack-work/figaro/api/form"
)

const studyTabCount = 10

const studiesPitName = "form studies"

type studyTab struct {
	form  string
	view  *formView
	close func()
}

type studiesPit struct {
	mu      sync.Mutex
	owner   string
	forms   []string
	more    int
	tabs    map[string]*studyTab
	lastOf  string
	current int
}

func studiedForms(snap form.Snapshot) []string {
	for _, key := range []string{form.StudiesKey, "system.studies"} {
		raw, ok := snap.Get(key)
		if !ok {
			continue
		}
		if entries, ok := listEntries(raw); ok {
			return entries
		}
	}
	return nil
}

func studyKeyIndex(b byte) int {
	if b == '0' {
		return studyTabCount - 1
	}
	return int(b - '1')
}

func (s *studiesPit) choose(owner string, snap form.Snapshot, want int) (int, string) {
	var closers []func()
	defer func() { runAll(closers) }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner != s.owner {
		closers = s.takeTabsLocked()
		s.owner, s.lastOf = owner, ""
	}
	all := studiedForms(snap)
	s.more = max(len(all)-studyTabCount, 0)
	s.forms = all[:len(all)-s.more]
	if len(s.forms) == 0 {
		return 0, "no studies: this figaro observes no form"
	}
	if want < 0 {
		want = 0
		for i, f := range s.forms {
			if f == s.lastOf {
				want = i
			}
		}
	}
	if want >= len(s.forms) {
		return 0, fmt.Sprintf("no study %d: this figaro studies %d", (want+1)%studyTabCount, len(all))
	}
	s.current, s.lastOf = want, s.forms[want]
	return want, ""
}

func (s *studiesPit) view(idx int, open mountOpener) (*formView, error) {
	s.mu.Lock()
	id := s.forms[idx]
	tab := s.tabs[id]
	if tab != nil && tab.view != nil && !tab.view.isStopped() {
		v := tab.view
		s.mu.Unlock()
		return v, nil
	}
	s.mu.Unlock()
	if tab != nil && tab.close != nil {
		tab.close()
	}
	v, _, closeConn, _, err := open(id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tabs == nil {
		s.tabs = map[string]*studyTab{}
	}
	s.tabs[id] = &studyTab{form: id, view: v, close: closeConn}
	return v, nil
}

func (s *studiesPit) loaded(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tabs[id] != nil
}

func (s *studiesPit) takeTabsLocked() []func() {
	var closers []func()
	for _, t := range s.tabs {
		if t.close != nil {
			closers = append(closers, t.close)
		}
	}
	s.tabs = nil
	return closers
}

func runAll(fns []func()) {
	for _, f := range fns {
		f()
	}
}

func (s *studiesPit) closeAll() {
	s.mu.Lock()
	closers := s.takeTabsLocked()
	s.mu.Unlock()
	runAll(closers)
}

func (s *studiesPit) strip() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var parts []string
	for i, id := range s.forms {
		label := fmt.Sprintf("%d %s", (i+1)%studyTabCount, s.nameLocked(id))
		if i == s.current {
			label = "[" + label + "]"
		}
		parts = append(parts, label)
	}
	out := strings.Join(parts, "  ")
	if s.more > 0 {
		out = fmt.Sprintf("+%d more  ", s.more) + out
	}
	return out
}

func (s *studiesPit) nameLocked(id string) string {
	if t := s.tabs[id]; t != nil && t.view != nil {
		if name := studyFormName(t.view.source().Shown()); name != "" {
			return name
		}
	}
	return shortHex(id)
}

func studyFormName(snap form.Snapshot) string {
	for _, key := range []string{"role-name", "name"} {
		raw, ok := snap.Get(key)
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
	}
	return ""
}

func (v *formView) isStopped() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.stopped
}

func (in *interactiveInput) openStudies(tab int) {
	if in.replaying {
		in.note("studies: not available in a replay")
		return
	}
	in.mu.Lock()
	owner := in.figaroID
	var snap form.Snapshot
	if in.bound != nil {
		snap, _, _ = in.bound.state()
	}
	in.mu.Unlock()
	idx, refusal := in.studies.choose(owner, snap, tab)
	if refusal != "" {
		in.note(refusal)
		return
	}
	finished := func() {}
	if !in.studies.loadedAt(idx) {
		finished = in.progressIfSlow("…studies")
	}
	view, err := in.studies.view(idx, in.mountForm)
	finished()
	if err != nil {
		in.pitFailed("studies: " + err.Error())
		return
	}
	view.subject = in.subjectState()
	if view.opener == nil && view.view == nil {
		view.opener = in.mountForm
	}
	view.strip = in.studies.strip
	in.installFormView(studiesPitName, view, func() {}, false)
}

func (s *studiesPit) loadedAt(idx int) bool {
	s.mu.Lock()
	id := s.forms[idx]
	s.mu.Unlock()
	return s.loaded(id)
}
