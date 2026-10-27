//go:build !unix

package cli

// processAlive cannot be determined portably; assume the run is live.
func processAlive(int) bool { return true }
