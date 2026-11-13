//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package cli

import "syscall"

const ioctlReadTermios = syscall.TIOCGETA
