//go:build linux

package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/perffix"
	"github.com/jack-work/figaro/internal/store"
)

const (
	perfPitFigaros = 200
	perfPitForms   = 24
)

// A store like the pager-roles snapshot: 200 conversations in fork trees up
// to four deep, every one with a mantra and a few board writes, and role
// forms aimed at some of them.
func perfPitStore(b *testing.B) (dir, attend string) {
	dir = perffix.Dir(b, "cli-pit-v1", func(dir string) {
		be, err := store.NewXwalBackend(dir, 0)
		if err != nil {
			b.Fatal(err)
		}
		defer be.Close()
		outfit, err := be.CreateOutfit("perf", form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"system.provider": json.RawMessage(`"anthropic"`),
			"system.model":    json.RawMessage(`"claude-sonnet-4-5"`),
		}, nil))
		if err != nil {
			b.Fatal(err)
		}
		minter := store.MintedBy(fmt.Sprintf("user:%d", os.Getuid()))
		ids := make([]string, 0, perfPitFigaros)
		for i := 0; i < perfPitFigaros; i++ {
			from := outfit
			if i%4 != 0 {
				from = ids[i-1]
			}
			id, _, err := be.ForkWith(from, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
				"mantra": json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("perf figaro %d tends the pit", i))),
			}, nil), minter)
			if err != nil {
				b.Fatal(err)
			}
			ids = append(ids, id)
			for p := 0; p < 6; p++ {
				raw, _ := json.Marshal(fmt.Sprintf("step %d of figaro %d", p, i))
				if _, err := be.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{
					fmt.Sprintf("plan.s%d", p%3): raw,
				}, nil)); err != nil {
					b.Fatal(err)
				}
			}
		}
		for i := 0; i < perfPitForms; i++ {
			if _, _, err := be.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{
				"role":        json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("role-%d", i))),
				"target-aria": json.RawMessage(fmt.Sprintf("%q", ids[i*7])),
				"status":      json.RawMessage(`"ready"`),
			}, nil), minter); err != nil {
				b.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "perf-attend"), []byte(ids[len(ids)-1]), 0o600); err != nil {
			b.Fatal(err)
		}
	})
	raw, err := os.ReadFile(filepath.Join(dir, "perf-attend"))
	if err != nil {
		b.Fatal(err)
	}
	return dir, string(raw)
}

type pitPager struct {
	b      *testing.B
	ptmx   *os.File
	marks  *os.File
	reader *bufio.Reader
	exited chan error
}

type keyPaint struct {
	M    string  `json:"m"`
	View string  `json:"view"`
	MS   float64 `json:"ms"`
}

func (p *pitPager) send(keys string) {
	p.b.Helper()
	if _, err := p.ptmx.Write([]byte(keys)); err != nil {
		p.b.Fatal(err)
	}
}

func (p *pitPager) nextPaint(why string) keyPaint {
	p.b.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var partial []byte
	for {
		line, err := p.reader.ReadBytes('\n')
		partial = append(partial, line...)
		if err == nil {
			var k keyPaint
			if json.Unmarshal(partial, &k) == nil && k.M == "key.paint" {
				return k
			}
			partial = partial[:0]
			continue
		}
		if err != io.EOF {
			p.b.Fatal(err)
		}
		select {
		case err := <-p.exited:
			p.b.Fatalf("%s: the pager exited (%v)", why, err)
		default:
		}
		if time.Now().After(deadline) {
			p.b.Fatalf("%s: no key.paint mark within 30s", why)
		}
		time.Sleep(time.Millisecond)
	}
}

func (p *pitPager) paintIn(view, why string) float64 {
	p.b.Helper()
	k := p.nextPaint(why)
	if k.View != view {
		p.b.Fatalf("%s: the key painted view %q, want %q", why, k.View, view)
	}
	return k.MS
}

func openPitPager(b *testing.B) *pitPager {
	src, attend := perfPitStore(b)
	r := newRestartRig(b)
	if err := os.CopyFS(filepath.Join(r.root, "state", "arias"), os.DirFS(src)); err != nil {
		b.Fatal(err)
	}
	var listed []json.RawMessage
	if err := json.Unmarshal([]byte(r.run("ls", "-j")), &listed); err != nil {
		b.Fatal(err)
	}
	if len(listed) < perfPitFigaros {
		b.Fatalf("the daemon lists %d figaros from the fixture, want >= %d", len(listed), perfPitFigaros)
	}

	marks := filepath.Join(r.root, "marks.log")
	c := r.cmd("listen", attend)
	c.Env = append(c.Env, "TERM=xterm-256color", "FIGARO_MARKS="+marks)
	ptmx, tty, err := pty.Open()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { ptmx.Close(); tty.Close() })
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 48, Cols: 160}); err != nil {
		b.Fatal(err)
	}
	c.Stdin, c.Stdout, c.Stderr = tty, tty, tty
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := c.Start(); err != nil {
		b.Fatal(err)
	}
	var screen listenScreen
	go func() { _, _ = io.Copy(&screen, ptmx) }()
	exited := make(chan error, 1)
	reaped := make(chan struct{})
	go func() {
		exited <- c.Wait()
		close(reaped)
	}()
	b.Cleanup(func() {
		_ = c.Process.Kill()
		<-reaped
	})
	deadline := time.Now().Add(30 * time.Second)
	for !screen.has("live") {
		select {
		case err := <-exited:
			b.Fatalf("the listen exited before painting (%v)", err)
		default:
		}
		if time.Now().After(deadline) {
			b.Fatalf("the pager never painted:\n%s", screen.tail(2000))
		}
		time.Sleep(20 * time.Millisecond)
	}
	f, err := os.Open(marks)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { f.Close() })
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		b.Fatal(err)
	}
	return &pitPager{b: b, ptmx: ptmx, marks: f, reader: bufio.NewReader(f), exited: exited}
}

// The perf gate's pit open: in a real pager on a pty, the time from the key
// that opens a pit (l, L, or the Enter of :ls) to the frame that first shows
// it, as the pager's own key.paint mark reports it. Nothing else is typed
// while the pit opens, so the mark carries that one key and no other. ns/op
// is the median open of the run.
func BenchmarkGatePitOpen(b *testing.B) {
	if testing.Short() {
		b.Skip("spawns a daemon and a pager")
	}
	p := openPitPager(b)
	for _, c := range []struct {
		name    string
		prelude string
		open    string
	}{
		{"l", "", "l"},
		{"L", "", "L"},
		{"colon-ls", ":ls", "\r"},
	} {
		b.Run(c.name, func(b *testing.B) {
			p.b = b
			opens := make([]float64, 0, b.N)
			for i := 0; i < b.N; i++ {
				for _, k := range c.prelude {
					p.send(string(k))
					p.paintIn("command", c.name+" typing the command")
				}
				p.send(c.open)
				opens = append(opens, p.paintIn("pit", c.name+" opening"))
				p.send("q")
				p.paintIn("transcript", c.name+" closing")
			}
			slices.Sort(opens)
			b.ReportMetric(opens[len(opens)/2]*1e6, "ns/op")
		})
	}
}
