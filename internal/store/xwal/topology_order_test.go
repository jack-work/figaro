package xwal

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTopologyLocksBeforePublishingRootMutation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *Trunks) func() error
	}{
		{
			name: "fork",
			setup: func(t *testing.T, f *Trunks) func() error {
				right, err := f.SpawnUnderStump("s")
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := f.Append(right, 0, []byte(`"right"`), nil); err != nil {
					t.Fatal(err)
				}
				return func() error {
					_, err := f.ForkTail(right)
					return err
				}
			},
		},
		{
			name: "ensure-channel",
			setup: func(_ *testing.T, f *Trunks) func() error {
				return func() error {
					return f.ensureChannel(ChannelSpec{
						Name: "turn-wal/ordered", Kind: ChannelLog,
						Opaque: true,
					})
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "f")
			f, trunk := seedTrunk(t, dir)
			topology := tc.setup(t, f)

			appendEntered := make(chan struct{})
			releaseAppend := make(chan struct{})
			f.testAfterReadLock = func() {
				close(appendEntered)
				<-releaseAppend
			}
			appendDone := make(chan error, 1)
			go func() {
				_, _, err := f.Append(trunk, 0, []byte(`"not-stranded"`), nil)
				appendDone <- err
			}()
			<-appendEntered

			topologyDone := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				topologyDone <- topology()
			}()
			<-started
			time.Sleep(20 * time.Millisecond)

			state := rootTopologyStateFor(f.registryRoot)
			state.mu.Lock()
			published := state.mutating
			state.mu.Unlock()

			close(releaseAppend)
			select {
			case appendErr := <-appendDone:
				if appendErr != nil {
					t.Fatal(appendErr)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("append stranded behind topology mutation")
			}

			if published {
				t.Fatal("topology mutation published before acquiring t.mu")
			}
			if err := <-topologyDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}
