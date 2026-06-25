//go:build windows

package process

import "os/exec"

// setProcessGroup is a no-op on Windows; process-group semantics differ
// and exec.CommandContext's default Kill is used.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessTree kills the child process. Windows does not share the
// POSIX process-group kill semantics, so grandchildren are not reaped
// here; WaitDelay still bounds how long Run blocks.
func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
