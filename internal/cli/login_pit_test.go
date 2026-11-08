package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/auth"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/sdk/aria"
)

type lockedSink struct {
	mu  sync.Mutex
	raw strings.Builder
	ft  *ldrender.FakeTerminal
}

func (s *lockedSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw.Write(p)
	return s.ft.Write(p)
}

func (s *lockedSink) everything() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.raw.String() + strings.Join(s.ft.Screen(), "\n")
}

func loginRig(t *testing.T, b loginBackend) (*interactiveInput, *lockedSink) {
	t.Helper()
	sink := &lockedSink{ft: ldrender.NewFakeTerminal(100, 30)}
	client := aria.NewClient()
	client.Apply(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 1, Inquiry: "q", Sealed: true}}}}, aria.Notify)
	tr := newTranscript(sink, 100, 30, ldrender.NodeText{}, client, "aria1234", time.Unix(0, 0))
	tr.enter()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	in := &interactiveInput{mu: new(renderLock), lt: &livelogTurn{tr: tr}, life: ctx, login: &loginSession{backend: b}}
	return in, sink
}

func (in *interactiveInput) eventually(t *testing.T, why string, cond func(tr *transcript, s *loginSession) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		in.mu.Lock()
		ok := cond(in.lt.tr, in.login)
		in.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", why)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (in *interactiveInput) keys(s string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	for i := 0; i < len(s); i++ {
		in.lt.tr.key(s[i])
	}
}

func oneProvider(state string, signedIn bool) func() ([]loginProvider, string) {
	return func() ([]loginProvider, string) {
		return []loginProvider{{name: "anthropic", state: state, in: signedIn}}, ""
	}
}

func askingForAKey(got chan<- string) func(context.Context, string, auth.Console) error {
	return func(ctx context.Context, _ string, c auth.Console) error {
		c.Note("paste a key from the console")
		key, err := c.AskSecret(ctx, "API key: ")
		if err != nil {
			return err
		}
		got <- string(key)
		c.Done("stored encrypted api key")
		return nil
	}
}

const pitSecret = "sk-ant-SECRET42"

func assertNoSecret(t *testing.T, in *interactiveInput, sink *lockedSink) {
	t.Helper()
	if strings.Contains(sink.everything(), "SECRET42") {
		t.Fatal("the secret reached the terminal")
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	tr := in.lt.tr
	for name, e := range map[string]*lineEditor{"command": &tr.cmdline, "draft": &tr.draft, "answer": &tr.askline} {
		if strings.Contains(e.String(), "SECRET42") || strings.Contains(strings.Join(e.history, "\n"), "SECRET42") ||
			strings.Contains(strings.Join(e.kills, "\n"), "SECRET42") {
			t.Fatalf("the secret is left in the %s editor", name)
		}
	}
	for _, n := range tr.notes.newestFirst() {
		if strings.Contains(n.text+n.detail, "SECRET42") {
			t.Fatalf("the secret is in a notification: %q", n.text)
		}
	}
	for _, l := range in.login.flow.lines {
		if strings.Contains(l.text, "SECRET42") {
			t.Fatalf("the secret is in a pit row: %q", l.text)
		}
	}
}

func TestAKeyTypedIntoTheLoginPitReachesTheFlowAndNothingElse(t *testing.T) {
	got := make(chan string, 1)
	in, sink := loginRig(t, loginBackend{list: oneProvider("not signed in", false), signIn: askingForAKey(got)})

	in.openLoginPit([]string{"anthropic"})
	in.eventually(t, "the question opens a box", func(tr *transcript, _ *loginSession) bool { return tr.box == boxAsk })
	in.keys(pitSecret)
	if !strings.Contains(sink.everything(), strings.Repeat("*", len(pitSecret))) {
		t.Fatal("the answer box never drew its mask")
	}
	in.keys("\x15")
	in.keys(pitSecret + "\r")

	select {
	case key := <-got:
		if key != pitSecret {
			t.Fatalf("the flow got %q", key)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the flow never got its answer")
	}
	in.eventually(t, "the sign-in finishes", func(_ *transcript, s *loginSession) bool { return !s.flow.running() })
	assertNoSecret(t, in, sink)
}

func TestAPasteWhileTheLoginPitWaitsGoesToTheQuestionNotTheComposer(t *testing.T) {
	got := make(chan string, 1)
	in, sink := loginRig(t, loginBackend{list: oneProvider("not signed in", false), signIn: askingForAKey(got)})

	in.openLoginPit([]string{"anthropic"})
	in.eventually(t, "the question opens a box", func(tr *transcript, _ *loginSession) bool { return tr.box == boxAsk })
	in.keys("\x1b")
	in.eventually(t, "Esc puts the box away and keeps the question", func(tr *transcript, s *loginSession) bool {
		return tr.box == boxNone && s.flow.ask != nil
	})
	in.mu.Lock()
	in.lt.tr.pasteText(pitSecret)
	if in.lt.tr.box != boxAsk || in.lt.tr.draft.String() != "" {
		in.mu.Unlock()
		t.Fatalf("the paste went to box %d, draft %q", in.lt.tr.box, in.lt.tr.draft.String())
	}
	in.mu.Unlock()
	in.keys("\r")
	if key := <-got; key != pitSecret {
		t.Fatalf("the flow got %q", key)
	}
	in.eventually(t, "the sign-in finishes", func(_ *transcript, s *loginSession) bool { return !s.flow.running() })
	assertNoSecret(t, in, sink)
}

func TestTheLoginPitShowsTheDeviceCodeAndXCancelsTheSignIn(t *testing.T) {
	in, _ := loginRig(t, loginBackend{
		list: oneProvider("not signed in", false),
		signIn: func(ctx context.Context, _ string, c auth.Console) error {
			c.Link("https://github.com/login/device")
			c.Code("ABCD-1234")
			<-ctx.Done()
			return ctx.Err()
		},
	})
	in.openLoginPit(nil)
	in.eventually(t, "the providers are listed", func(_ *transcript, s *loginSession) bool { return s.listed })
	in.mu.Lock()
	in.lt.tr.pit.focus(loginRowID("anthropic"))
	in.mu.Unlock()
	in.keys("\r")
	in.eventually(t, "the device code shows", func(tr *transcript, _ *loginSession) bool {
		return strings.Contains(strings.Join(tr.pit.lines(tr.w, 30), "\n"), "code  ABCD-1234")
	})
	in.mu.Lock()
	in.lt.tr.pit.focus(loginRowID("anthropic"))
	in.mu.Unlock()
	in.keys("x")
	in.eventually(t, "x cancels the sign-in", func(_ *transcript, s *loginSession) bool {
		return !s.flow.running() && s.flow.lines[len(s.flow.lines)-1].text == "cancelled"
	})
}

func TestSigningOutOfAProviderAsksFirst(t *testing.T) {
	out := make(chan string, 1)
	in, _ := loginRig(t, loginBackend{
		list: oneProvider("sealed api_key in providers/anthropic.toml", true),
		signOut: func(name string) (logoutReport, error) {
			out <- name
			return logoutReport{}, errors.New("nothing to remove")
		},
	})
	in.openLoginPit(nil)
	in.eventually(t, "the providers are listed", func(_ *transcript, s *loginSession) bool { return s.listed })
	in.mu.Lock()
	in.lt.tr.pit.focus(loginRowID("anthropic"))
	in.mu.Unlock()
	in.keys("x")
	select {
	case <-out:
		t.Fatal("signed out without asking")
	default:
	}
	in.eventually(t, "a confirmation is up", func(tr *transcript, _ *loginSession) bool { return tr.confirm != nil })
	in.keys("y")
	if name := <-out; name != "anthropic" {
		t.Fatalf("signed out of %q", name)
	}
}

func TestThePagerRoutesLoginVerbsToThePit(t *testing.T) {
	for _, line := range []string{"login", "provider login"} {
		in, _ := loginRig(t, loginBackend{list: oneProvider("not signed in", false)})
		in.execCommand(line)
		in.eventually(t, line+" opens the login pit", func(tr *transcript, _ *loginSession) bool { return tr.showing(pitLogin) })
	}
}

func TestTheLoginPitUnderTestCannotOpenABrowser(t *testing.T) {
	if auth.BrowserAllowed() || liveLoginBackend(nil).open("https://example.invalid/") {
		t.Fatal("the test fence leaves the live opener able to launch a browser")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"WXYZ-0001","verification_uri":"https://idp.invalid/device",` +
			`"verification_uri_complete":"https://idp.invalid/device?code=WXYZ-0001","interval":5,"expires_in":60}`))
	}))
	defer srv.Close()
	opened := make(chan string, 4)
	in, _ := loginRig(t, loginBackend{
		open: func(u string) bool { opened <- u; return false },
		list: oneProvider("not signed in", false),
		signIn: func(ctx context.Context, _ string, c auth.Console) error {
			_, err := auth.LoginDeviceCode(ctx, auth.DeviceCodeConfig{ClientID: "c", DeviceURL: srv.URL, TokenURL: srv.URL}, c)
			return err
		},
	})
	in.openLoginPit([]string{"anthropic"})
	select {
	case u := <-opened:
		if u != "https://idp.invalid/device?code=WXYZ-0001" {
			t.Fatalf("the pit's opener got %q", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the device flow never asked the pit to open its link")
	}
	in.eventually(t, "the link and code are rows", func(tr *transcript, _ *loginSession) bool {
		pit := strings.Join(tr.pit.lines(tr.w, 30), "\n")
		return strings.Contains(pit, "https://idp.invalid/device?code=WXYZ-0001") && strings.Contains(pit, "code  WXYZ-0001")
	})
	in.mu.Lock()
	in.lt.tr.pit.focus(loginRowID("anthropic"))
	in.mu.Unlock()
	in.keys("x")
	in.eventually(t, "x cancels the sign-in", func(_ *transcript, s *loginSession) bool { return !s.flow.running() })
}

const osc52 = "\x1b]52;c;cGF5bG9hZA==\x07"

func TestRemoteTextInTheQuestionBoxAndTheBarPaintsNoEscape(t *testing.T) {
	got := make(chan string, 1)
	in, sink := loginRig(t, loginBackend{
		list: oneProvider("not signed in", false),
		signIn: func(ctx context.Context, _ string, c auth.Console) error {
			c.Note("visit" + osc52 + " now")
			c.Link("https://idp.invalid/device" + osc52)
			key, err := c.AskSecret(ctx, "API key: ")
			if err != nil {
				return err
			}
			got <- string(key)
			return errors.New("refused by the provider" + osc52)
		},
	})
	in.openLoginPit([]string{"anthropic"})
	in.eventually(t, "the question opens a box", func(tr *transcript, _ *loginSession) bool { return tr.box == boxAsk })
	in.mu.Lock()
	title := strings.Join(in.lt.tr.askTitle(), "\n")
	in.lt.tr.render()
	in.mu.Unlock()
	if !strings.Contains(title, "visit now") || strings.Contains(title, "]52;") {
		t.Fatalf("question box title %q", title)
	}
	in.keys("k\r")
	<-got
	in.eventually(t, "the failure reaches the bar", func(tr *transcript, s *loginSession) bool {
		return !s.flow.running() && strings.Contains(tr.status.noticeText(), "refused by the provider")
	})
	in.mu.Lock()
	in.lt.tr.render()
	in.mu.Unlock()
	if strings.Contains(sink.everything(), "]52;") {
		t.Fatal("an OSC 52 from remote text reached the terminal")
	}
}
