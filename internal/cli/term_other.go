//go:build !(darwin || freebsd || netbsd || openbsd || dragonfly || linux)

package cli

import "os"

// isTerminalFile falls back to a character-device check where Sift has no
// termios query. The null device is a character device too, but nobody
// answers on it.
func isTerminalFile(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}
