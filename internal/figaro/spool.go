package figaro

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
)

const ResumeNotice = "continue: the daemon restarted mid-turn. The last tool result may be lost, " +
	"so check the current state before running a tool again."

var ErrStopping = errors.New(rpc.DaemonStopping + ": send again once it is back")

const resumeSender = "angelus"

const spoolSlowWarn = time.Second

const StateSuspended = "suspended"

type Spool struct {
	dir  string
	hold func()
}

func NewSpool(dir string) *Spool {
	if dir == "" {
		return nil
	}
	return &Spool{dir: dir}
}

type spoolFile struct {
	Queued []spooledPrompt `json:"queued,omitempty"`
	Cut    *cutTurn        `json:"cut,omitempty"`
}

type spooledPrompt struct {
	Text     string            `json:"text,omitempty"`
	Images   []message.Content `json:"images,omitempty"`
	Form     *rpc.FormInput    `json:"form,omitempty"`
	Segments []spooledSegment  `json:"segments,omitempty"`
	At       int64             `json:"at,omitempty"`
	Resume   bool              `json:"resume,omitempty"`
	Keys     []string          `json:"keys,omitempty"`
}

type spooledSegment struct {
	Sender    string `json:"sender,omitempty"`
	Principal string `json:"principal,omitempty"`
	Text      string `json:"text,omitempty"`
	Notice    bool   `json:"notice,omitempty"`
	Delivery  string `json:"delivery,omitempty"`
}

type cutTurn struct {
	Turn      uint64 `json:"turn"`
	Resumed   bool   `json:"resumed,omitempty"`
	Suspended bool   `json:"suspended,omitempty"`
}

func (c *cutTurn) owesResume() bool {
	return c != nil && (c.Suspended || !c.Resumed)
}

func (s *Spool) path(ariaID string) string {
	return filepath.Join(s.dir, ariaID+".json")
}

func (s *Spool) read(ariaID string) ([]byte, spoolFile, error) {
	data, err := os.ReadFile(s.path(ariaID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, spoolFile{}, nil
	}
	if err != nil {
		return nil, spoolFile{}, err
	}
	var f spoolFile
	if err := json.Unmarshal(data, &f); err != nil {
		return data, spoolFile{}, fmt.Errorf("spool %s: %w", ariaID, err)
	}
	return data, f, nil
}

func (s *Spool) write(ariaID string, data []byte) error {
	if s.hold != nil {
		s.hold()
	}
	if data == nil {
		return s.Remove(ariaID)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "."+ariaID+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), s.path(ariaID))
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

func (s *Spool) Prepare() error {
	if s == nil {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.dir, 0o700); err != nil {
		return err
	}
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			if err := os.Remove(filepath.Join(s.dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Spool) Remove(ariaID string) error {
	if s == nil {
		return nil
	}
	err := os.Remove(s.path(ariaID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Spool) Holds(ariaID string) bool {
	if s == nil {
		return false
	}
	_, err := os.Stat(s.path(ariaID))
	return !errors.Is(err, fs.ErrNotExist)
}

func (s *Spool) Arias() ([]string, error) {
	if s == nil {
		return nil, nil
	}
	ents, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, ".json"))
	}
	return ids, nil
}

func spooled(e event) spooledPrompt {
	p := spooledPrompt{Text: e.text, Images: e.images, Form: e.form, At: e.at, Resume: e.resume, Keys: e.keys}
	for _, s := range e.segments {
		p.Segments = append(p.Segments, spooledSegment{Sender: s.sender, Principal: s.principal, Text: s.text, Notice: s.notice, Delivery: s.delivery})
	}
	return p
}

func (p spooledPrompt) event() event {
	e := event{typ: eventUserPrompt, text: p.Text, images: p.Images, form: p.Form, at: p.At, resume: p.Resume, keys: p.Keys}
	for _, s := range p.Segments {
		e.segments = append(e.segments, promptSegment{sender: s.Sender, principal: s.Principal, text: s.Text, notice: s.Notice, delivery: s.Delivery})
	}
	return e
}

func acceptKey() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (a *Agent) acceptedSince(queued []spooledPrompt) map[string]bool {
	if len(queued) == 0 {
		return nil
	}
	oldest := queued[0].At
	for _, p := range queued {
		oldest = min(oldest, p.At)
	}
	landed := map[string]bool{}
	before := ^uint64(0)
	for {
		page, _ := a.figLog.ReadPage(0, before, 64)
		if len(page) == 0 {
			return landed
		}
		for i := len(page) - 1; i >= 0; i-- {
			m := page[i].Payload
			if m.Timestamp != 0 && m.Timestamp < oldest {
				return landed
			}
			for _, k := range m.Accepted {
				landed[k] = true
			}
		}
		if page[0].LT == 0 {
			return landed
		}
		before = page[0].LT
	}
}

func resumeEvent() event {
	return event{
		typ:      eventUserPrompt,
		text:     ResumeNotice,
		resume:   true,
		segments: []promptSegment{{sender: resumeSender, text: ResumeNotice}},
	}
}

func (a *Agent) Redeliver() {
	if a.spool == nil {
		return
	}
	a.spoolMu.Lock()
	data, f, err := a.spool.read(a.id)
	if err != nil {
		slog.Error("spool unreadable; its prompts are not redelivered", "aria", a.id, "err", err)
	}
	a.spoolLast = data
	a.spoolArmed = true
	a.spoolMu.Unlock()

	var events []event
	switch {
	case a.isLog():
	case f.Cut.owesResume():
		events = append(events, resumeEvent())
	case f.Cut != nil:
		slog.Warn("a resumed turn was cut again by a crash; not resuming it twice", "aria", a.id, "turn", f.Cut.Turn)
	}
	landed := a.acceptedSince(f.Queued)
	for _, p := range f.Queued {
		if len(p.Keys) > 0 && landed[p.Keys[0]] {
			continue
		}
		events = append(events, p.event())
	}
	if len(events) > 0 {
		slog.Info("redelivering after restart", "aria", a.id,
			"queued", len(f.Queued), "resume", f.Cut.owesResume())
	}
	a.inbox.SendAll(events)
	a.persistSpool()
}

func (a *Agent) persistSpool() {
	if a.spool == nil {
		return
	}
	a.spoolMu.Lock()
	defer a.spoolMu.Unlock()
	if !a.spoolArmed {
		return
	}
	f := spoolFile{Cut: a.cut}
	for _, e := range a.inbox.Unanswered() {
		f.Queued = append(f.Queued, spooled(e))
	}
	var data []byte
	if len(f.Queued) > 0 || f.Cut != nil {
		var err error
		if data, err = json.Marshal(f); err != nil {
			slog.Error("spool encode", "aria", a.id, "err", err)
			return
		}
	}
	if !a.spoolStale && bytes.Equal(data, a.spoolLast) {
		return
	}
	a.spoolLast, a.spoolStale = data, false
	a.queueSpoolWrite(data)
}

// queueSpoolWrite hands the newest snapshot to the one writer, under spoolMu so
// an older snapshot can never land after a newer one.
func (a *Agent) queueSpoolWrite(data []byte) {
	a.spoolWMu.Lock()
	a.spoolPending, a.spoolDirty = data, true
	if a.spoolWriting {
		a.spoolWMu.Unlock()
		return
	}
	a.spoolWriting = true
	a.spoolIdle = make(chan struct{})
	a.spoolWMu.Unlock()
	go a.drainSpool()
}

func (a *Agent) drainSpool() {
	for {
		a.spoolWMu.Lock()
		if !a.spoolDirty {
			a.spoolWriting = false
			close(a.spoolIdle)
			a.spoolWMu.Unlock()
			return
		}
		data := a.spoolPending
		a.spoolPending, a.spoolDirty = nil, false
		a.spoolWMu.Unlock()

		started := time.Now()
		err := a.spool.write(a.id, data)
		if took := time.Since(started); took >= spoolSlowWarn {
			slog.Warn("spool write slow", "aria", a.id, "ms", took.Milliseconds())
		}
		if err != nil {
			slog.Error("spool write; a restart now loses this aria's queue", "aria", a.id, "err", err)
			a.spoolMu.Lock()
			a.spoolStale = true
			a.spoolMu.Unlock()
		}
	}
}

// FlushSpool waits for the spool writer to land the newest snapshot.
func (a *Agent) FlushSpool() {
	a.spoolWMu.Lock()
	if !a.spoolWriting {
		a.spoolWMu.Unlock()
		return
	}
	idle := a.spoolIdle
	a.spoolWMu.Unlock()
	<-idle
}

func (a *Agent) markCut(turn uint64, resumed bool) {
	a.spoolMu.Lock()
	if !a.halted.Load() {
		a.cut = &cutTurn{Turn: turn, Resumed: resumed}
	}
	a.spoolMu.Unlock()
}

func (a *Agent) settleCut() {
	a.mu.RLock()
	hungUp := a.hungUp
	a.mu.RUnlock()
	a.spoolMu.Lock()
	if a.cut != nil && a.suspended.Load() && !hungUp && !a.halted.Load() {
		a.cut.Suspended = true
	} else {
		a.cut = nil
	}
	a.spoolMu.Unlock()
	a.persistSpool()
}
