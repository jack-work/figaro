package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
)

type loginLineKind uint8

const (
	loginNote loginLineKind = iota
	loginLink
	loginCode
	loginDone
	loginFail
)

type loginLine struct {
	kind loginLineKind
	text string
}

type loginFlow struct {
	provider string
	verb     string
	lines    []loginLine
	cancel   context.CancelFunc
	ask      *boxQuestion
	done     bool
}

func (f *loginFlow) running() bool { return f != nil && !f.done }

type loginProvider struct {
	name  string
	state string
	in    bool
}

type loginBackend struct {
	list    func() ([]loginProvider, string)
	signIn  func(ctx context.Context, name string, c auth.Console) error
	signOut func(name string) (logoutReport, error)
}

func liveLoginBackend(loaded *config.Loaded) loginBackend {
	return loginBackend{
		list: func() ([]loginProvider, string) { return loginRows(loaded) },
		signIn: func(ctx context.Context, name string, c auth.Console) error {
			reg, err := loginTarget(loaded, name)
			if err != nil {
				return err
			}
			if err := pagerHush(); err != nil {
				return err
			}
			return signIn(ctx, loaded, name, reg, c)
		},
		signOut: func(name string) (logoutReport, error) {
			store, err := pagerOAuthStore()
			if err != nil {
				return logoutReport{}, err
			}
			rep, err := providerLogout(loaded, name, store)
			if err == nil {
				relistProvider(name)
			}
			return rep, err
		},
	}
}

type loginSession struct {
	backend   loginBackend
	providers []loginProvider
	listed    bool
	hushNote  string
	flow      *loginFlow
	rev       uint64
}

var errHushAsleep = errors.New("hush is not running, and the pager cannot ask for the vault passphrase: run figaro provider login <name> in a shell")

func pagerHush() error {
	h, err := boot.Hush()
	if err != nil {
		return err
	}
	if h.Client().Ping() != nil {
		return errHushAsleep
	}
	return nil
}

func pagerOAuthStore() (oauthStore, error) {
	h, err := boot.Hush()
	if err != nil {
		return nil, err
	}
	if !h.HasIdentity() {
		return noOAuth{}, nil
	}
	if h.Client().Ping() != nil {
		return nil, errHushAsleep
	}
	return h.Client(), nil
}

func loginRows(loaded *config.Loaded) ([]loginProvider, string) {
	store, err := pagerOAuthStore()
	note := ""
	if err != nil {
		store, note = noOAuth{}, "hush is not running: oauth sign-ins are not shown"
	}
	var rows []loginProvider
	for _, name := range providerPkg.Names() {
		srcs, err := providerSources(loaded, name, store)
		switch {
		case err != nil:
			rows = append(rows, loginProvider{name: name, state: err.Error()})
		case len(srcs) == 0:
			rows = append(rows, loginProvider{name: name, state: "not signed in"})
		default:
			rows = append(rows, loginProvider{name: name, state: describeSource(srcs[0]), in: true})
		}
	}
	return rows, note
}

func (in *interactiveInput) loginState() *loginSession {
	if in.login == nil {
		in.login = &loginSession{backend: liveLoginBackend(in.loaded)}
	}
	return in.login
}

func (in *interactiveInput) loginChanged() {
	in.loginState().rev++
	in.lt.render()
}

func (in *interactiveInput) relistLogin() {
	in.mu.Lock()
	list := in.loginState().backend.list
	in.mu.Unlock()
	rows, note := list()
	in.mu.Lock()
	defer in.mu.Unlock()
	s := in.loginState()
	s.providers, s.hushNote, s.listed = rows, note, true
	in.lt.tr.signIns = signInsNone
	for _, p := range rows {
		if p.in {
			in.lt.tr.signIns = signInsSome
		}
	}
	in.loginChanged()
}

func (in *interactiveInput) openLoginPit(args []string) {
	switch {
	case len(args) > 1:
		in.noteErr("login takes at most one provider (:login, or :login <provider>)")
		return
	case len(args) == 1:
		if _, err := lookupProvider(args[0]); err != nil {
			in.noteErr(err.Error())
			return
		}
	}
	in.mu.Lock()
	in.loginState()
	in.lt.tr.closePanels()
	in.lt.tr.showLivePit(string(pitLogin), &loginView{in: in}, false)
	if len(args) == 1 {
		in.startLogin(args[0])
	}
	in.mu.Unlock()
	go in.relistLogin()
}

func (in *interactiveInput) askLogout(name string) {
	if _, err := lookupProvider(name); err != nil {
		in.noteErr(err.Error())
		return
	}
	in.openLoginPit(nil)
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.lt.tr.showing(pitLogin) {
		in.lt.tr.pit.focus(loginRowID(name))
		in.lt.tr.askConfirm(logoutPrompt(name), func() { in.startLogout(name) })
	}
}

func logoutPrompt(name string) string {
	return "sign out of " + name + "? figaro forgets its copy of the credential"
}

func loginRowID(name string) string { return "p:" + name }

func (in *interactiveInput) startLogin(name string) {
	s := in.loginState()
	if s.flow.running() {
		in.lt.tr.note(fmt.Sprintf("a sign-in to %s is under way: x on its row cancels it", s.flow.provider))
		return
	}
	life := in.life
	if life == nil {
		life = context.Background()
	}
	ctx, cancel := context.WithCancel(life)
	f := &loginFlow{provider: name, verb: "signing in", cancel: cancel}
	s.flow = f
	in.loginChanged()
	go in.runLogin(ctx, f, s.backend.signIn)
}

func (in *interactiveInput) runLogin(ctx context.Context, f *loginFlow, signIn func(context.Context, string, auth.Console) error) {
	defer f.cancel()
	err := signIn(ctx, f.provider, &pitConsole{in: in, f: f})
	in.mu.Lock()
	in.finishFlow(f, err, ctx.Err() != nil, "signed in")
	in.mu.Unlock()
	in.relistLogin()
}

func (in *interactiveInput) finishFlow(f *loginFlow, err error, cancelled bool, success string) {
	f.done = true
	if f.ask != nil {
		in.lt.tr.retractAsk(f.ask)
		f.ask = nil
	}
	switch {
	case cancelled:
		f.lines = append(f.lines, loginLine{loginFail, "cancelled"})
	case err != nil:
		f.lines = append(f.lines, loginLine{loginFail, err.Error()})
		in.lt.tr.setCommandNoteAt(f.provider+": "+err.Error(), alertError)
	default:
		f.lines = append(f.lines, loginLine{loginDone, success})
		in.lt.tr.setCommandNote(f.provider + ": " + success)
	}
	in.loginChanged()
}

func (in *interactiveInput) startLogout(name string) {
	s := in.loginState()
	if s.flow.running() {
		in.lt.tr.note(fmt.Sprintf("a sign-in to %s is under way: x on its row cancels it", s.flow.provider))
		return
	}
	f := &loginFlow{provider: name, verb: "signing out", cancel: func() {}}
	s.flow = f
	in.loginChanged()
	go in.runLogout(f, s.backend.signOut)
}

func (in *interactiveInput) runLogout(f *loginFlow, signOut func(string) (logoutReport, error)) {
	rep, err := signOut(f.provider)
	in.mu.Lock()
	for _, src := range rep.Removed {
		f.lines = append(f.lines, loginLine{loginNote, "removed " + describeSource(src)})
	}
	if len(rep.Removed) > 0 {
		f.lines = append(f.lines, loginLine{loginNote, "what " + f.provider + " issued stays valid there until you revoke it"})
	}
	for _, src := range rep.Remaining {
		f.lines = append(f.lines, loginLine{loginNote, "kept " + describeSource(src) + ": " + leaveHint(src)})
	}
	success := "signed out"
	switch {
	case len(rep.Remaining) > 0:
		success = "still signed in through " + describeSource(rep.Remaining[0])
	case len(rep.Removed) == 0:
		success = "was not signed in"
	}
	in.finishFlow(f, err, false, success)
	in.mu.Unlock()
	in.relistLogin()
}

type pitConsole struct {
	in *interactiveInput
	f  *loginFlow
}

func (c *pitConsole) add(kind loginLineKind, text string) {
	c.in.mu.Lock()
	defer c.in.mu.Unlock()
	c.f.lines = append(c.f.lines, loginLine{kind, text})
	c.in.loginChanged()
}

func (c *pitConsole) Note(s string) { c.add(loginNote, s) }
func (c *pitConsole) Link(u string) { c.add(loginLink, u) }
func (c *pitConsole) Code(s string) { c.add(loginCode, s) }
func (c *pitConsole) Done(s string) { c.add(loginDone, s) }

func (c *pitConsole) Ask(ctx context.Context, prompt string) (string, error) {
	return c.ask(ctx, prompt, false)
}

func (c *pitConsole) AskSecret(ctx context.Context, prompt string) ([]byte, error) {
	s, err := c.ask(ctx, prompt, true)
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

func (c *pitConsole) ask(ctx context.Context, prompt string, secret bool) (string, error) {
	reply := make(chan string, 1)
	q := &boxQuestion{
		prompt: c.f.provider + " · " + prompt,
		secret: secret,
		title:  c.f.recent(3),
		answer: func(s string) { reply <- s },
	}
	c.in.mu.Lock()
	c.f.ask = q
	c.in.lt.tr.pose(q)
	c.in.loginChanged()
	c.in.mu.Unlock()
	var answer string
	var err error
	select {
	case answer = <-reply:
	case <-ctx.Done():
		err = ctx.Err()
	}
	c.in.mu.Lock()
	c.in.lt.tr.retractAsk(q)
	if c.f.ask == q {
		c.f.ask = nil
	}
	c.in.loginChanged()
	c.in.mu.Unlock()
	return strings.TrimSpace(answer), err
}

func (f *loginFlow) recent(n int) []string {
	var out []string
	for _, l := range f.lines[max(len(f.lines)-n, 0):] {
		out = append(out, l.row())
	}
	return out
}

func (l loginLine) row() string {
	switch l.kind {
	case loginCode:
		return "code  " + l.text
	case loginDone:
		return "✓ " + l.text
	case loginFail:
		return "✗ " + l.text
	}
	return l.text
}

type loginView struct{ in *interactiveInput }

func (v *loginView) Close() {}

func (v *loginView) Revision() uint64 { return v.in.loginState().rev }

func (v *loginView) Items(width int) []pitRow {
	s := v.in.loginState()
	col := len("provider")
	for _, p := range s.providers {
		col = max(col, len(p.name))
	}
	rows := []pitRow{staticRow(fmt.Sprintf("    %-*s  %s", col, "provider", "sign-in"))}
	if !s.listed {
		rows = append(rows, staticRow("  …reading what each provider holds"))
	}
	for _, p := range s.providers {
		mark := "·"
		switch {
		case s.flow.running() && s.flow.provider == p.name:
			mark = "…"
		case p.in:
			mark = "✓"
		}
		rows = append(rows, pitRow{id: loginRowID(p.name), text: fmt.Sprintf("  %s %-*s  %s", mark, col, p.name, p.state)})
	}
	if s.hushNote != "" {
		rows = append(rows, pitRow{text: "  " + s.hushNote, tone: toneWarn})
	}
	if f := s.flow; f != nil {
		head := f.provider
		if f.running() {
			head += ": " + f.verb
		}
		rows = append(rows, staticRow(""), staticRow("  "+head))
		for i, l := range f.lines {
			r := pitRow{text: "    " + l.row()}
			switch l.kind {
			case loginLink, loginCode:
				r.id, r.yank = fmt.Sprintf("l:%d", i), l.text
			case loginFail:
				r.tone = toneError
			}
			rows = append(rows, r)
		}
		if f.ask != nil && v.in.lt.tr.ask == f.ask && !v.in.lt.tr.asking() {
			rows = append(rows, pitRow{id: "ask", text: "  ▸ answer: " + f.ask.prompt + "  (Enter)"})
		}
	}
	hint := "  Enter signs in · x signs out · y copies a row · Esc closes"
	switch {
	case s.flow.running() && s.flow.verb == "signing out":
		hint = "  Esc closes, the sign-out goes on"
	case s.flow.running():
		hint = "  x on " + s.flow.provider + " cancels · y copies a row · Esc closes, the sign-in goes on"
	}
	return append(rows, staticRow(""), staticRow(hint))
}

func (v *loginView) Activate(id string) {
	in := v.in
	switch {
	case id == "ask":
		in.lt.tr.reopenAsk()
	case strings.HasPrefix(id, "p:"):
		in.startLogin(strings.TrimPrefix(id, "p:"))
	}
}

func (v *loginView) DropPrompt(id string) (string, bool) {
	name, ok := strings.CutPrefix(id, "p:")
	if !ok {
		return "", false
	}
	s := v.in.loginState()
	if s.flow.running() && s.flow.provider == name {
		return "", true
	}
	return logoutPrompt(name), true
}

func (v *loginView) Drop(id string) {
	name, _ := strings.CutPrefix(id, "p:")
	s := v.in.loginState()
	if s.flow.running() && s.flow.provider == name {
		s.flow.cancel()
		return
	}
	v.in.startLogout(name)
}
