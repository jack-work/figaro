package angelus

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/store"
)

type slowRefsBackend struct {
	store.Backend
	checking chan struct{}
	release  chan struct{}
	need     bool
	migrated chan struct{}
}

func (b *slowRefsBackend) RefsNeedMigration() bool {
	close(b.checking)
	<-b.release
	return b.need
}

func (b *slowRefsBackend) ReconcileLibrettos() (store.LibrettoAudit, error) {
	close(b.migrated)
	return store.LibrettoAudit{}, nil
}

// The check reads every libretto (0.4 s on 1,500 arias) and ran before Serve,
// so every client waited on it after a restart.
func TestTheLibrettoRefsCheckDoesNotHoldBackServing(t *testing.T) {
	b := &slowRefsBackend{checking: make(chan struct{}), release: make(chan struct{}), need: true, migrated: make(chan struct{})}
	a := &Angelus{Backend: b}
	returned := make(chan struct{})
	go func() {
		a.migrateLibrettoRefs()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		close(b.release)
		t.Fatal("migrateLibrettoRefs waited on the refs check: Serve starts only after it")
	}
	<-b.checking
	close(b.release)
	select {
	case <-b.migrated:
	case <-time.After(2 * time.Second):
		t.Fatal("refs that need migration were not migrated")
	}
}
