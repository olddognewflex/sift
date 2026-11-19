package storage

import (
	"fmt"
	"os"

	"github.com/olddognewflex/sift/internal/contract"
)

// EffectiveStatus detects runs left "running" by a process that no longer
// exists (killed, crashed, machine restarted). The note explains a status
// that differs from, or qualifies, the recorded one.
func EffectiveStatus(m *contract.Manifest) (contract.RunStatus, string) {
	if m.Status != contract.RunRunning {
		return m.Status, ""
	}
	host, _ := os.Hostname()
	if m.Host == host && !processAlive(m.PID) {
		return contract.RunInterrupted, fmt.Sprintf("the audit process (pid %d) exited without finishing; artifacts written so far are preserved", m.PID)
	}
	if m.Host != host {
		return m.Status, fmt.Sprintf("run is executing on host %s", m.Host)
	}
	return m.Status, fmt.Sprintf("in progress (pid %d)", m.PID)
}
