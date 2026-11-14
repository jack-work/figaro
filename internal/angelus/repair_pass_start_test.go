package angelus_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/store"
)

type passStartProbe struct {
	store.Backend
	socket  func() string
	started chan error
}

func (p *passStartProbe) StartRepairPass() {
	conn, err := net.Dial("unix", p.socket())
	if err == nil {
		conn.Close()
	}
	p.started <- err
}

// The crash repair pass competes with boot at one core, so the daemon starts
// it only once its socket answers.
func TestTheDaemonStartsTheRepairPassOnceTheSocketAnswers(t *testing.T) {
	dir := t.TempDir()
	probe := &passStartProbe{Backend: store.NewTestBackend(t), started: make(chan error, 2)}
	a := angelus.New(angelus.Config{Backend: probe, RuntimeDir: testRuntimeDir(t, dir)})
	probe.socket = func() string { return a.SocketPath }

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(3 * time.Second):
			t.Error("angelus.Run did not return after cancel")
		}
	}()

	select {
	case err := <-probe.started:
		if err != nil {
			t.Fatalf("the repair pass started before the socket answered: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon never started the repair pass")
	}
	waitForAngelus(t, a.SocketPath)
	select {
	case <-probe.started:
		t.Fatal("the repair pass was started twice")
	default:
	}
}
