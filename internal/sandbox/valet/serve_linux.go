//go:build linux

package valet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jack-work/figaro/internal/sandbox/box"
	"golang.org/x/sys/unix"
)

const (
	connFD = 3
	planFD = 4

	openingDeadline  = 10 * time.Second
	strangerDeadline = 2 * time.Second
)

type Plan struct {
	Fence Fence  `json:"fence"`
	Read  []byte `json:"read"`
}

var scopeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var scrubbed = []string{"NOTIFY_SOCKET", "LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR", "SSH_AUTH_SOCK"}

func PeerUID(nc *net.UnixConn) (int, error) {
	raw, err := nc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	if cerr := raw.Control(func(fd uintptr) {
		cred, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); cerr != nil {
		return 0, cerr
	}
	if err != nil {
		return 0, err
	}
	return int(cred.Uid), nil
}

func (v Valet) Answer(nc *net.UnixConn, now time.Time) (*exec.Cmd, error) {
	uid, err := PeerUID(nc)
	if err != nil {
		return nil, fmt.Errorf("valet: peer credentials: %w", err)
	}
	stranger := !slices.Contains(v.Peers, uid)
	budget := openingDeadline
	if stranger {
		budget = strangerDeadline
	}
	if err := nc.SetReadDeadline(time.Now().Add(budget)); err != nil {
		return nil, err
	}
	o, err := box.ReadOpening(nc)
	if stranger {
		refusal := fmt.Errorf("valet: uid %d may not call this valet", uid)
		if err == nil {
			o.Refuse(nc, refusal)
		}
		return nil, refusal
	}
	if err != nil {
		return nil, err
	}
	if err := nc.SetReadDeadline(time.Time{}); err != nil {
		return nil, err
	}
	refuse := func(err error) (*exec.Cmd, error) {
		o.Refuse(nc, err)
		return nil, err
	}
	if !scopeName.MatchString(o.Scope) {
		return refuse(fmt.Errorf("valet: %q does not name a figaro", o.Scope))
	}
	if err := Available(); err != nil {
		return refuse(err)
	}
	tab, err := Load(v.State)
	if err != nil {
		return refuse(fmt.Errorf("valet: %w", err))
	}
	fence, err := v.fenceFor(o.Scope, tab.For(o.Scope, now, v.MaxGrant))
	if err != nil {
		return refuse(fmt.Errorf("valet: %w", err))
	}
	cmd, err := v.start(nc, Plan{Fence: fence, Read: o.Read})
	if err != nil {
		return refuse(fmt.Errorf("valet: start the fence: %w", err))
	}
	return cmd, nil
}

func (v Valet) fenceFor(scope string, grants []Grant) (Fence, error) {
	root := filepath.Join(v.State, "root")
	scratch := filepath.Join(v.State, "scratch", scope)
	for _, d := range []string{root, scratch} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return Fence{}, err
		}
	}
	hidden := append(slices.Clone(v.Hide), v.State)
	var live []Grant
	for _, g := range grants {
		if h, ok := Overlaps(g.Path, hidden); ok {
			fmt.Fprintf(v.Log, "valet: %s: grant of %s dropped: it overlaps %s, which no tool sees\n", scope, g.Path, h)
			continue
		}
		if _, err := os.Stat(g.Path); err == nil {
			live = append(live, g)
		} else {
			fmt.Fprintf(v.Log, "valet: %s: grant of %s skipped: %v\n", scope, g.Path, err)
		}
	}
	door := ""
	if v.Door != "" {
		if _, err := os.Stat(v.Door); err == nil {
			door = v.Door
		}
	}
	return Fence{
		Root:    root,
		Home:    v.Home,
		Scratch: scratch,
		Door:    door,
		Grants:  GrantMounts(live),
		Hide:    hidden,
	}, nil
}

func (v Valet) start(nc *net.UnixConn, plan Plan) (*exec.Cmd, error) {
	conn, err := nc.File()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	planFile, err := writePlan(plan)
	if err != nil {
		return nil, err
	}
	defer planFile.Close()
	uid, gid := os.Getuid(), os.Getgid()
	cmd := exec.Command(v.Self, "valet", "fence")
	cmd.Env = os.Environ()
	cmd.Stdout, cmd.Stderr = v.Log, v.Log
	cmd.ExtraFiles = []*os.File{conn, planFile}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		GidMappingsEnableSetgroups: false,
		AmbientCaps:                []uintptr{unix.CAP_SYS_ADMIN},
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func writePlan(plan Plan) (*os.File, error) {
	data, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	fd, err := unix.MemfdCreate("valet-plan", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "valet-plan")
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func readPlan() (Plan, *os.File, error) {
	f := os.NewFile(planFD, "valet-plan")
	if f == nil {
		return Plan{}, nil, errors.New("valet: no plan was handed over")
	}
	var plan Plan
	data, err := io.ReadAll(io.NewSectionReader(f, 0, 1<<30))
	if err == nil {
		err = json.Unmarshal(data, &plan)
	}
	if err != nil {
		f.Close()
		return Plan{}, nil, fmt.Errorf("valet: plan: %w", err)
	}
	return plan, f, nil
}

func RunFence() error {
	plan, planFile, err := readPlan()
	if err != nil {
		return err
	}
	conn := os.NewFile(connFD, "connection")
	exe, err := os.Open("/proc/self/exe")
	if err == nil {
		err = plan.Fence.Build()
	}
	if err != nil {
		if o, perr := box.ReadOpening(bytes.NewReader(plan.Read)); perr == nil {
			o.Refuse(conn, fmt.Errorf("valet: %w", err))
		}
		return err
	}
	runtime.LockOSThread()
	if err := dropCapabilities(); err != nil {
		return fmt.Errorf("valet: drop capabilities: %w", err)
	}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains(scrubbed, k)
	})
	err = syscall.Exec("/proc/self/fd/"+strconv.Itoa(int(exe.Fd())), []string{"figaro", "valet", "fenced"}, env)
	runtime.KeepAlive(conn)
	runtime.KeepAlive(planFile)
	return fmt.Errorf("valet: exec inside the fence: %w", err)
}

func RunFenced(ctx context.Context) error {
	plan, planFile, err := readPlan()
	if err != nil {
		return err
	}
	planFile.Close()
	f := os.NewFile(connFD, "connection")
	nc, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("valet: connection: %w", err)
	}
	if err := plan.Fence.Lock(); err != nil {
		if o, perr := box.ReadOpening(bytes.NewReader(plan.Read)); perr == nil {
			_ = o.Refuse(nc, fmt.Errorf("valet: %w", err))
		}
		nc.Close()
		return err
	}
	srv, err := box.NewServer(plan.Fence.Scratch)
	if err != nil {
		return err
	}
	if plan.Fence.Door != "" {
		if err := srv.CallBackThrough(plan.Fence.Door); err != nil {
			return err
		}
	}
	srv.ServeConn(ctx, nc, plan.Read)
	return nil
}

func Notify(state string) (bool, error) {
	sock := os.Getenv("NOTIFY_SOCKET")
	if sock == "" {
		return false, nil
	}
	if sock[0] == '@' {
		sock = "\x00" + sock[1:]
	}
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		return false, err
	}
	defer c.Close()
	if _, err := c.Write([]byte(state)); err != nil {
		return false, err
	}
	r, w, err := os.Pipe()
	if err == nil {
		oob := unix.UnixRights(int(w.Fd()))
		_, _, berr := c.WriteMsgUnix([]byte("BARRIER=1"), oob, nil)
		w.Close()
		if berr == nil {
			_ = r.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, _ = io.Copy(io.Discard, r)
		}
		r.Close()
	}
	return true, nil
}

func dropCapabilities() error {
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return err
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return err
	}
	data[0].Inheritable, data[1].Inheritable = 0, 0
	if err := unix.Capset(&hdr, &data[0]); err != nil {
		return err
	}
	return unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
}
