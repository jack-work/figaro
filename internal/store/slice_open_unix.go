//go:build unix

package store

import "syscall"

const openNoFollow = syscall.O_NOFOLLOW
