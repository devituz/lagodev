//go:build !windows

package process

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in a new process group so the whole
// tree can be signalled at once.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessTree sends SIGKILL to the child's entire process group,
// reaping backgrounded grandchildren that would otherwise be orphaned
// and keep the stdout/stderr pipe open.
func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	// Negative pid targets the process group whose ID equals pid (set up
	// by Setpgid above). Fall back to the single process on failure.
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
