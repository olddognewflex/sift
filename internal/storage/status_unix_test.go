//go:build unix

package storage

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/olddognewflex/sift/internal/contract"
)

func TestEffectiveStatusDeadPID(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot run true: %v", err)
	}
	host, _ := os.Hostname()
	m := manifest("20261027T100000Z-aaaaaa")
	m.Status, m.Host, m.PID = contract.RunRunning, host, cmd.Process.Pid
	st, note := EffectiveStatus(m)
	if st != contract.RunInterrupted || !strings.Contains(note, "exited without finishing") {
		t.Fatalf("dead pid = %s %q, want interrupted", st, note)
	}
}
