//go:build linux

package cli

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// A replay ran pager verbs for real: S dialled the local daemon through
// `form show`, and against an empty root that meant ensureHush, the vault's
// first-run wizard drawn over the pager, keys eaten as a passphrase and the
// replay gone. Every key the keymap binds and every verb the router knows,
// pressed in a replay against an empty root, must be answered from the tape
// or refused with a line: no daemon, no vault, no wizard, no crash.
func TestAReplayNeverLeavesItsTape(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the binary in a pty; skipped under -short")
	}
	root, err := os.MkdirTemp("/var/tmp", "figaro-offtape-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	for _, sub := range []string{"run", "state", "cache", "config", "hush"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tapePath := offTapeFixture(t, root)

	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "FIGARO_") || strings.HasPrefix(k, "ANTHROPIC_") || strings.HasPrefix(k, "HUSH_") || k == "TMUX" || k == "EDITOR" || k == "VISUAL" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"FIGARO_RUNTIME_DIR="+filepath.Join(root, "run"),
		"FIGARO_NO_ANGELUS_SCOPE=1",
		"HUSH_NO_AGENT_SCOPE=1",
		"FIGARO_STATE_DIR="+filepath.Join(root, "state"),
		"FIGARO_CACHE_DIR="+filepath.Join(root, "cache"),
		"FIGARO_CONFIG_DIR="+filepath.Join(root, "config"),
		"FIGARO_HUSH_APP=figaro-test-offtape",
		"FIGARO_HUSH_DIR="+filepath.Join(root, "hush"),
		"FIGARO_NO_SELF_SPAWN=1",
		"FIGARO_AMBIGUOUS_WIDE=0",
		"EDITOR=true", "VISUAL=true",
		"TERM=xterm-256color",
	)
	replay := exec.Command(smokeBinary(t), "replay", tapePath, "--speed", "50")
	replay.Env = env
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ptmx.Close(); tty.Close() })
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	replay.Stdin, replay.Stdout, replay.Stderr = tty, tty, tty
	replay.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Pdeathsig: syscall.SIGKILL}
	if err := replay.Start(); err != nil {
		t.Fatal(err)
	}
	out := &listenScreen{}
	go func() { _, _ = io.Copy(out, ptmx) }()
	exited := make(chan error, 1)
	go func() { exited <- replay.Wait() }()
	t.Cleanup(func() {
		select {
		case <-exited:
		default:
			_ = replay.Process.Kill()
			<-exited
		}
	})

	if !screenShows(out, "opera.md", 20*time.Second) {
		t.Fatalf("the replay never drew the tape; screen tail:\n%q", out.tail(800))
	}
	press := func(keys string) {
		if _, err := ptmx.Write([]byte(keys)); err != nil {
			t.Fatalf("write %q: %v", keys, err)
		}
		time.Sleep(15 * time.Millisecond)
	}
	alive := func(after string) {
		t.Helper()
		select {
		case err := <-exited:
			t.Fatalf("the replay ended after %s (%v); screen tail:\n%q", after, err, out.tail(1500))
		default:
		}
		for _, leak := range []string{"Passphrase", "secrets vault", "goroutine ", "refusing to spawn"} {
			if out.has(leak) {
				t.Fatalf("after %s the screen shows %q; screen tail:\n%q", after, leak, out.tail(1500))
			}
		}
	}

	press("S")
	if !screenShows(out, "answered-from-the-tape", 10*time.Second) {
		alive("S")
		t.Fatalf("S never drew the form the tape recorded; screen tail:\n%q", out.tail(1500))
	}
	press("\x1b")
	alive("S")

	for _, opener := range []string{"", "S", " n", "h", "!"} {
		for i := range keymap {
			bd := &keymap[i]
			reach := inPanel | inList
			if opener == "" {
				reach = inTranscript
			}
			if bd.modes&reach == 0 || offTapeExits(bd) {
				continue
			}
			keys := bindingInput(bd)
			if bd.prefix != "" {
				lead := prefixKey(bd.prefix)
				if lead == "" {
					continue
				}
				keys = lead + keys
			}
			press(opener)
			press(keys)
			press("\x1b")
			press("\x1b")
		}
		alive("the keymap with " + strconv.Quote(opener) + " open")
	}

	lines := append(buildRouter("figaro", nil).CommandNames(),
		"comment hello", "form show deadbeef", "state deadbeef/queue", "attend deadbeef",
		"listen deadbeef", "send -- hi", "fork", "new -- hi", "kill", "succeed", "decide",
		"models", "reaped", "troupe", "ls", "vault unlock", "provider login", "notifications",
	)
	for _, line := range lines {
		press(":")
		press(line)
		press("\t")
		press("\r")
		press("\x1b")
		alive(":" + line)
	}
	if !screenShows(out, "a replay has only its tape", 5*time.Second) {
		t.Fatalf("no verb was refused with a line; screen tail:\n%q", out.tail(1500))
	}
	alive("every verb")

	for _, dir := range []string{"run", "hush", "config"} {
		entries, _ := os.ReadDir(filepath.Join(root, dir))
		for _, e := range entries {
			t.Errorf("the replay left %s/%s in the root: it reached the daemon, the vault or the config", dir, e.Name())
		}
	}
}

func offTapeExits(bd *keyBinding) bool {
	switch bd.help {
	case helpLeavePit, helpDetach, helpInterrupt:
		return true
	}
	return false
}

func prefixKey(name string) string {
	for i := range keymap {
		if keymap[i].action == "prefix:"+name {
			return bindingInput(&keymap[i])
		}
	}
	return ""
}

func offTapeFixture(t *testing.T, root string) string {
	t.Helper()
	src, err := os.Open(filepath.Join("testdata", "tapes", "stream-eager.tape"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(root, "offtape.tape")
	dst, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	first := true
	for sc.Scan() {
		dst.WriteString(sc.Text() + "\n")
		if first {
			first = false
			dst.WriteString(`{"t":0.0001,"dir":"out","msg":{"jsonrpc":"2.0","id":900,"method":"figaro.form","params":{"figaro_id":"e4219ad4"}}}` + "\n")
			dst.WriteString(`{"t":0.0002,"dir":"in","msg":{"jsonrpc":"2.0","id":900,"result":{"snapshot":{"aria_id":"e4219ad4","tape_marker":"answered-from-the-tape"},"version":1}}}` + "\n")
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	dst.WriteString(`{"t":3600,"dir":"in","msg":{"jsonrpc":"2.0","method":"tape.hold","params":{}}}` + "\n")
	return path
}
