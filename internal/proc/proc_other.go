//go:build !unix

package proc

import (
	"os"
	"os/exec"
)

// Process groups are a Unix feature. Elsewhere, cancellation stops only the command itself.

func startInOwnGroup(*exec.Cmd) {}

func terminateGroup(p *os.Process) { _ = p.Kill() } // best effort: the process may already be gone

func killGroup(p *os.Process) { _ = p.Kill() } // best effort: the process may already be gone
