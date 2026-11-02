//go:build linux

package upstream

import (
	"errors"
	"log/slog"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func watchRoutes(changed func()) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	groups := uint32(unix.RTMGRP_IPV4_IFADDR | unix.RTMGRP_IPV6_IFADDR | unix.RTMGRP_IPV4_ROUTE | unix.RTMGRP_IPV6_ROUTE)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		unix.Close(fd)
		return err
	}
	f := os.NewFile(uintptr(fd), "netlink-route")
	go func() {
		defer f.Close()
		buf := make([]byte, 64<<10)
		for {
			n, err := f.Read(buf)
			if errors.Is(err, unix.ENOBUFS) {
				changed()
				continue
			}
			if err != nil {
				slog.Warn("provider connections stopped following network changes", "err", err)
				return
			}
			if routingChanged(buf[:n]) {
				changed()
			}
		}
	}()
	return nil
}

func routingChanged(b []byte) bool {
	msgs, err := syscall.ParseNetlinkMessage(b)
	if err != nil {
		return true
	}
	for _, m := range msgs {
		switch m.Header.Type {
		case unix.RTM_NEWADDR, unix.RTM_DELADDR, unix.RTM_NEWROUTE, unix.RTM_DELROUTE:
			return true
		}
	}
	return false
}
