//go:build unix

package proc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// startInOwnGroup starts the command in a new process group, so cancellation can signal everything it
// starts, not just the command itself.
func startInOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateGroup(p *os.Process) { signalGroup(p, syscall.SIGTERM) }

func killGroup(p *os.Process) { signalGroup(p, syscall.SIGKILL) }

// signalGroup signals the process group that p leads. A group that has already exited is fine.
func signalGroup(p *os.Process, sig syscall.Signal) {
	err := syscall.Kill(-p.Pid, sig)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return
	}
	_ = p.Signal(sig) // best effort: signal at least the command itself
}
