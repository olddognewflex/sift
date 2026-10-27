package conformance

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// lingering returns the PIDs of processes whose working directory is under
// root. Every harness process starts under the check's directory (in the
// snapshot, or in the harness's private TMPDIR) and children inherit it, so
// after Run returns this list must be empty.
//
// Limit: a process that detaches and changes directory (normal daemonizing)
// escapes this check. Tagging processes by environment variable would catch
// it, but macOS hides the environment of system binaries.
//
// The check waits briefly first: a process killed just before Run returned
// may take a moment to disappear from the process table.
func lingering(root string) ([]int, error) {
	var pids []int
	var err error
	for range 10 {
		pids, err = processesUnder(root)
		if err != nil || len(pids) == 0 {
			return pids, err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return pids, nil
}

func processesUnder(root string) ([]int, error) {
	switch runtime.GOOS {
	case "linux":
		return procCwds(root)
	case "darwin", "freebsd", "openbsd", "netbsd":
		return lsofCwds(root)
	}
	return nil, errors.New("leftover-process detection is not implemented on " + runtime.GOOS)
}

func under(root, dir string) bool {
	return dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))
}

func procCwds(root string) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		cwd, err := os.Readlink(filepath.Join("/proc", e.Name(), "cwd"))
		if err == nil && under(root, cwd) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// lsofCwds parses `lsof -F pn` output: a "p<pid>" line starts each process
// and an "n<path>" line names its working directory.
func lsofCwds(root string) ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-n", "-P", "-w", "-d", "cwd", "-F", "pn").Output()
	// lsof exits 1 when some processes could not be inspected; the output
	// for our own processes is still complete.
	if err != nil && len(out) == 0 {
		return nil, err
	}
	var pids []int
	pid := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "n") && pid != 0 && pid != os.Getpid():
			if under(root, line[1:]) {
				pids = append(pids, pid)
			}
		}
	}
	return pids, sc.Err()
}
