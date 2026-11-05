//go:build !windows

package cli

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A process tree the way /proc shows a few tmux panes: figaro as a pane's own
// process, figaro under the pane's shell, figaro under nix develop's bash
// under the pane's shell, a pane with no figaro at all, and the daemon (which
// lives under no pane but must never be taken for a client if it did).
func upgradeFixture() ([]tmuxPane, procTable) {
	procs := []procInfo{
		{PID: 100, PPID: 1, Exe: "/nix/store/x-figaro/bin/figaro", Argv: []string{"figaro", "listen"}, Cwd: "/home/g", Stdin: "/dev/pts/1", Env: []string{"FIGARO_DEV_ROOT=/var/tmp/d"}},
		{PID: 200, PPID: 1, PGRP: 200, TPGID: 200, Exe: "/usr/bin/fish", Argv: []string{"-fish"}},
		{PID: 201, PPID: 200, Exe: "/nix/store/x-figaro/bin/figaro", Argv: []string{"figaro", "send", "--id", "abc", "--", "it's late"}, Cwd: "/w", Stdin: "/dev/pts/2"},
		{PID: 300, PPID: 1, Exe: "/usr/bin/fish", Argv: []string{"-fish"}},
		{PID: 301, PPID: 300, Exe: "/usr/bin/bash", Argv: []string{"bash"}},
		{PID: 302, PPID: 301, Exe: "/var/tmp/figaro-dev/bin/figaro", Argv: []string{"figaro", "form", "listen"}, Stdin: "/dev/pts/3"},
		{PID: 400, PPID: 1, Exe: "/usr/bin/fish", Argv: []string{"-fish"}},
		{PID: 401, PPID: 400, Exe: "/usr/bin/vim", Argv: []string{"vim"}},
		{PID: 500, PPID: 1, Exe: "/usr/bin/fish", Argv: []string{"-fish"}},
		{PID: 501, PPID: 500, Exe: "/nix/store/x-figaro/bin/figaro", Argv: []string{"figaro", "send", "--stdin"}, Stdin: "pipe:[77]"},
		{PID: 600, PPID: 1, Exe: "/usr/bin/fish", Argv: []string{"-fish"}},
		{PID: 601, PPID: 600, Exe: "/nix/store/x-figaro/bin/figaro", Argv: []string{"figaro", "upgrade"}, Stdin: "/dev/pts/6"},
		{PID: 700, PPID: 1, Exe: "/nix/store/x-figaro/bin/figaro", Daemon: true},
	}
	t := procTable{byPID: map[int]procInfo{}, children: map[int][]int{}}
	for _, p := range procs {
		t.byPID[p.PID] = p
		t.children[p.PPID] = append(t.children[p.PPID], p.PID)
	}
	panes := []tmuxPane{
		{ID: "%1", PID: 100, Command: "figaro"},
		{ID: "%2", PID: 200, Command: "figaro"},
		{ID: "%3", PID: 300, Command: "bash"},
		{ID: "%4", PID: 400, Command: "vim"},
		{ID: "%5", PID: 500, Command: "figaro"},
		{ID: "%6", PID: 600, Command: "figaro"},
		{ID: "%7", PID: 700, Command: "figaro"},
	}
	return panes, t
}

func TestUpgradeFindsFigaroInEveryPaneShapeAndNothingElse(t *testing.T) {
	panes, procs := upgradeFixture()
	clients, skipped := findClients(panes, procs, 601)
	byPane := map[string]paneClient{}
	for _, c := range clients {
		byPane[c.Pane] = c
	}
	if _, ok := byPane["%4"]; ok {
		t.Fatal("a pane running vim was taken for a figaro client")
	}
	if _, ok := byPane["%7"]; ok {
		t.Fatal("the daemon was taken for a client")
	}
	if _, ok := byPane["%6"]; ok {
		t.Fatal("the pane running this upgrade was planned for a restart")
	}
	if len(skipped) != 1 || !strings.HasPrefix(skipped[0], "%6") {
		t.Fatalf("this upgrade's own pane is named as skipped: got %v", skipped)
	}
	if c := byPane["%1"]; !c.Own || c.Cwd != "/home/g" || len(c.Env) != 1 {
		t.Fatalf("figaro as the pane's process: %+v", c)
	}
	if c := byPane["%2"]; c.Own || c.ShellPID != 200 || c.Shell != "fish" {
		t.Fatalf("figaro under the pane's shell: %+v", c)
	}
	if c := byPane["%3"]; c.ShellPID != 301 || c.Shell != "bash" {
		t.Fatalf("figaro under nix develop's bash: the shell to type into is bash: %+v", c)
	}
	if c := byPane["%5"]; !c.StdinPiped {
		t.Fatalf("a piped figaro: %+v", c)
	}
}

func TestUpgradePlanRespawnsWhatItCanReplayAndLeavesTheRest(t *testing.T) {
	panes, procs := upgradeFixture()
	clients, _ := findClients(panes, procs, 601)
	want := map[string]upgradeAction{"%1": upgradeRespawn, "%2": upgradeRespawn, "%3": upgradeRespawn, "%5": upgradeLeave}
	for _, c := range clients {
		s := planClient(c)
		if s.Action != want[c.Pane] {
			t.Errorf("%s (%v): got %s (%s), want %s", c.Pane, c.Argv, s.Action, s.Reason, want[c.Pane])
		}
	}
	orphan := paneClient{Pane: "%9", Argv: []string{"figaro"}}
	if s := planClient(orphan); s.Action != upgradeLeave {
		t.Errorf("figaro neither the pane's process nor a shell's child is left alone: got %s", s.Action)
	}
	stdin := paneClient{Pane: "%9", Own: true, Argv: []string{"figaro", "--stdin"}}
	if s := planClient(stdin); s.Action != upgradeLeave {
		t.Errorf("--stdin is not replayed: got %s", s.Action)
	}
}

// What upgrade types into a pane must reach the program as the argv it read
// from /proc, in every shell it types into.
func TestUpgradeQuotingRoundTripsThroughTheShell(t *testing.T) {
	argv := []string{"figaro", "send", "--id", "abc", "--", "it's late", `back\slash`, "$HOME", "two  spaces", "", "a;b|c"}
	for _, shell := range []string{"sh", "bash", "fish"} {
		path, err := exec.LookPath(shell)
		if err != nil {
			t.Logf("%s not installed; skipped", shell)
			continue
		}
		line := commandLineFor(argv[1:], shell)
		script := "for a in " + line + "; printf '%s\\0' \"$a\"; end"
		if shell != "fish" {
			script = "for a in " + line + "; do printf '%s\\0' \"$a\"; done"
		}
		out, err := exec.Command(path, "-c", script).Output()
		if err != nil {
			t.Fatalf("%s: %v", shell, err)
		}
		got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
		if strings.Join(got, "|") != strings.Join(argv[1:], "|") {
			t.Errorf("%s: %q came back as %q", shell, argv[1:], got)
		}
	}
}

// An upgrade must not cut a running turn or drop a queued prompt: it waits
// for the arias to go quiet, and refuses when they don't unless forced.
func TestUpgradeWaitsForRunningTurnsAndRefusesWithoutForce(t *testing.T) {
	calls := 0
	quietAfterTwo := func() ([]string, error) {
		calls++
		if calls <= 2 {
			return []string{"able-baker (a long turn)"}, nil
		}
		return nil, nil
	}
	if err := waitQuiet(upgradeOptions{wait: time.Second}, quietAfterTwo, time.Millisecond); err != nil {
		t.Fatalf("a turn that ends inside the wait: %v", err)
	}
	if calls != 3 {
		t.Fatalf("asked %d times, want 3", calls)
	}

	busy := func() ([]string, error) { return []string{"able-baker (a long turn)"}, nil }
	err := waitQuiet(upgradeOptions{wait: 5 * time.Millisecond}, busy, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "able-baker") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("a turn still running is refused, naming the aria and --force: %v", err)
	}
	if err := waitQuiet(upgradeOptions{wait: 5 * time.Millisecond, force: true}, busy, time.Millisecond); err != nil {
		t.Fatalf("--force goes ahead: %v", err)
	}
}

// A pager stopped mid-edit prints how many form edits went unconfirmed; the
// upgrade repeats those lines so they are not lost with the pane's scrollback.
func TestUpgradeCollectsUnconfirmedEditLines(t *testing.T) {
	screen := "bash-5.3$ figaro\n2 form edit(s) to @notes were not confirmed\nbash-5.3$ \n"
	got := unconfirmedLines("%3", screen)
	if len(got) != 1 || got[0] != "%3: 2 form edit(s) to @notes were not confirmed" {
		t.Fatalf("got %q", got)
	}
	if got := unconfirmedLines("%3", "bash-5.3$ \n"); len(got) != 0 {
		t.Fatalf("nothing to repeat: got %q", got)
	}
}

// A command line is replayed with send-keys -l, which delivers every byte as a
// keystroke: the terminal acts on a control character (^C aborts, ^U kills
// the line, CR submits, ESC and CSI drive the line editor) before the shell's
// quoting ever sees it. So an argv holding one is never typed again; the
// pane is named instead. argv here is what any local process can choose.
func TestUpgradeNeverTypesACommandLineThatHoldsAControlCharacter(t *testing.T) {
	for _, arg := range []string{"x\x03rm -rf ~\r", "a\rb", "\x1b[200~", "csi\u009bJ", "del\x7f", "nl\n", "bad\xffutf8"} {
		c := paneClient{Pane: "%9", ShellPID: 200, Shell: "bash", Argv: []string{"figaro", "send", "--", arg}}
		if s := planClient(c); s.Action != upgradeLeave {
			t.Errorf("%q: got %s (%s), want leave", arg, s.Action, s.Reason)
		}
	}
	if got := shownArgv([]string{"figaro\x03rm", "\x1b]0;x\x07", "\u009b2J"}); holdsControl(got) {
		t.Errorf("the report would write a control character to the terminal: %q", got)
	}
	clean := paneClient{Pane: "%9", ShellPID: 200, Shell: "bash", Argv: []string{"figaro", "send", "--", "plain words, ünïcode, it's fine"}}
	if s := planClient(clean); s.Action != upgradeRespawn {
		t.Errorf("a printable command line is still replayed: got %s (%s)", s.Action, s.Reason)
	}
}
