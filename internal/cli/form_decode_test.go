package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form/replica"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/jkrpc"
)

const oldShapeForm = `{"schema":1,"snapshot":{},"version":3,"view":{"rows":"*.models.*"}}`

// An older daemon of the same minor answered figaro.form with a view whose
// rows were a string. The decode error was swallowed: the pit retried forever
// under "the form has not loaded yet", and figaro models printed a bare
// "json: cannot unmarshal". Every form read must say the daemon is another
// build, name both builds, and say to run figaro stop.
func TestAnOlderDaemonsFormNamesBothBuildsAndFigaroStop(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "door.sock")
	var calls atomic.Int32
	fakeRPCServer(t, sock, map[string]jkrpc.HandlerFunc{
		rpc.MethodForm: func(context.Context, json.RawMessage) (interface{}, error) {
			calls.Add(1)
			return json.RawMessage(oldShapeForm), nil
		},
	})
	daemonSeen.Lock()
	known, same, who, d := daemonSeen.known, daemonSeen.same, daemonSeen.who, daemonSeen.d
	daemonSeen.Unlock()
	t.Cleanup(func() {
		daemonSeen.Lock()
		daemonSeen.known, daemonSeen.same, daemonSeen.who, daemonSeen.d = known, same, who, d
		daemonSeen.Unlock()
	})
	recordDaemon("the running angelus", daemonIdentity{build: "0ld0ld0ld0ld0ld0", release: "0.54.0"}, boot.BuildRevision())

	fcli, err := sdk.DialAria(sdk.Addr{Door: transport.UnixEndpoint(sock), ID: "fake-aria"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fcli.Close()

	wants := func(t *testing.T, where, msg string) {
		t.Helper()
		for _, want := range []string{"figaro stop", "daemon 0.54.0 (0ld0ld0ld0ld)", "cli    ", "another build"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: %q lacks %q", where, msg, want)
			}
		}
	}

	t.Run("a form read", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := fcli.Form(ctx)
		if !undecodable(err) {
			t.Fatalf("err = %v, want a decode error", err)
		}
		wants(t, "figaro.form", err.Error())
	})

	t.Run("a read-only pit", func(t *testing.T) {
		v := &formView{mirror: &formMirror{}, open: map[string]bool{}, refetch: func() (*rpc.FormResponse, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return fcli.FormOf(ctx, "")
		}}
		v.resync()
		v.noticeUnlessStopped("the form has not loaded yet")
		var drawn []string
		for _, r := range v.Items(200) {
			drawn = append(drawn, r.str())
		}
		got := strings.Join(drawn, "\n")
		wants(t, "the pit", got)
		if strings.Contains(got, "has not loaded yet") {
			t.Errorf("the pit hid the reason under the loading notice: %q", got)
		}
	})

	t.Run("the replica stops retrying and says why", func(t *testing.T) {
		before := calls.Load()
		failed := make(chan error, 1)
		r := replica.New("pit-test", ariaTransport{aria: func() *sdk.Aria { return fcli }}, replica.Options{
			Final:    undecodable,
			OnFailed: func(err error) { failed <- err },
		})
		defer r.Close()
		select {
		case err := <-failed:
			wants(t, "the replica", err.Error())
		case <-time.After(3 * time.Second):
			t.Fatal("the replica never reported the undecodable form: it is still retrying")
		}
		time.Sleep(300 * time.Millisecond)
		if n := calls.Load() - before; n != 1 {
			t.Errorf("the replica asked %d times; an answer that cannot decode is asked for once", n)
		}
	})
}
