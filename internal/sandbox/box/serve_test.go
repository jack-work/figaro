package box

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/jkrpc"
)

func sockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "box")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestServeSpeaksPlainJSONRPC(t *testing.T) {
	dir := sockDir(t)
	sock := filepath.Join(dir, "s")
	srv, err := NewServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, ln)

	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var out strings.Builder
	exited := make(chan exitNote, 1)
	c := jkrpc.NewClient(jkrpc.NewConn(nc), func(method string, params json.RawMessage) {
		switch method {
		case nChunk:
			var n chunkNote
			json.Unmarshal(params, &n)
			mu.Lock()
			out.Write(n.Data)
			mu.Unlock()
		case nExit:
			var n exitNote
			json.Unmarshal(params, &n)
			exited <- n
		}
	})
	defer c.Close()
	if err := c.Call(ctx, mExec, execReq{Handle: "x", Command: "true"}, nil); err == nil {
		t.Fatal("exec before open was served")
	}
	var opened openResp
	if err := c.Call(ctx, mOpen, openReq{Scope: "raw"}, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.Workspace != dir || opened.Isolation.Provider != "local" {
		t.Fatalf("open = %+v", opened)
	}
	if err := c.Call(ctx, mExec, execReq{Handle: "x", Command: "echo one; echo two; exit 4"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-exited:
		mu.Lock()
		got := out.String()
		mu.Unlock()
		if n.Handle != "x" || n.Result.ExitCode != 4 || got != "one\ntwo\n" {
			t.Fatalf("exit %+v after %q", n, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no exit notification")
	}
}

func TestCommandsInABoxCallBackThroughItsDoor(t *testing.T) {
	dir := sockDir(t)
	srv, err := NewServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.CallBackThrough(filepath.Join(dir, "door", "angelus.sock")); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, ln)
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var out strings.Builder
	exited := make(chan struct{})
	c := jkrpc.NewClient(jkrpc.NewConn(nc), func(method string, params json.RawMessage) {
		switch method {
		case nChunk:
			var n chunkNote
			json.Unmarshal(params, &n)
			mu.Lock()
			out.Write(n.Data)
			mu.Unlock()
		case nExit:
			close(exited)
		}
	})
	defer c.Close()
	env := map[string]string{"FIGARO_TROUPE": "unix:///elsewhere", "FIGARO_RUNTIME_DIR": "/elsewhere"}
	if err := c.Call(ctx, mOpen, openReq{Scope: "s", Env: env}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, mExec, execReq{Handle: "x", Command: `echo "$FIGARO_TROUPE $FIGARO_RUNTIME_DIR"`}, nil); err != nil {
		t.Fatal(err)
	}
	<-exited
	mu.Lock()
	defer mu.Unlock()
	want := "unix://" + filepath.Join(dir, "door", "angelus.sock") + " " + filepath.Join(dir, "door") + "\n"
	if out.String() != want {
		t.Fatalf("env %q, want %q: the box's door wins over what the daemon sends", out.String(), want)
	}
}
