//go:build darwin || freebsd || netbsd || openbsd || dragonfly || linux

package cli

import (
	"os"
	"syscall"
	"unsafe"
)

// isTerminalFile reports whether f is a terminal: reading its terminal
// attributes succeeds only on a tty, not on a pipe, file, or other
// character device such as /dev/null or /dev/zero. It uses SyscallConn
// rather than Fd, which would switch f to blocking mode.
func isTerminalFile(f *os.File) bool {
	rc, err := f.SyscallConn()
	if err != nil {
		return false
	}
	ok := false
	if err := rc.Control(func(fd uintptr) {
		var t syscall.Termios
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlReadTermios, uintptr(unsafe.Pointer(&t)))
		ok = errno == 0
	}); err != nil {
		return false
	}
	return ok
}
