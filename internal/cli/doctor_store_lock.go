package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/jack-work/figaro/internal/boot"
)

var doctorStoreLockWait = 2 * time.Second

func holdStoreForDoctor(verb string) (release func(), err error) {
	if err := os.MkdirAll(boot.StateDir(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(boot.StoreLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(doctorStoreLockWait)
	for boot.TryLockFile(f) != nil {
		if time.Now().After(deadline) {
			f.Close()
			who, _, held := boot.StoreLockHolder()
			if !held {
				who = "a process that let go as this one gave up"
			}
			return nil, fmt.Errorf("doctor %s: the store lock is held by %s; nothing was changed (if that is the daemon, figaro stop first)", verb, who)
		}
		time.Sleep(50 * time.Millisecond)
	}
	boot.RecordStoreLockOwner(f)
	return func() { f.Close() }, nil
}
