//go:build linux

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

func watchSocketCreated(path string) (<-chan struct{}, func()) {
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return nil, func() {}
	}
	if _, err := unix.InotifyAddWatch(fd, filepath.Dir(path), unix.IN_CREATE|unix.IN_MOVED_TO); err != nil {
		unix.Close(fd)
		return nil, func() {}
	}
	f := os.NewFile(uintptr(fd), "inotify")
	name := []byte(filepath.Base(path))
	created := make(chan struct{}, 1)
	go func() {
		buf := make([]byte, 64*(unix.SizeofInotifyEvent+unix.NAME_MAX+1))
		for {
			n, err := f.Read(buf)
			if err != nil {
				return
			}
			for off := 0; off+unix.SizeofInotifyEvent <= n; {
				ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
				end := off + unix.SizeofInotifyEvent + int(ev.Len)
				got := bytes.TrimRight(buf[off+unix.SizeofInotifyEvent:end], "\x00")
				off = end
				if bytes.Equal(got, name) {
					select {
					case created <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	return created, func() { f.Close() }
}
