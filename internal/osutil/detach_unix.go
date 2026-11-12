//go:build !windows

package osutil

import (
	"os"
	"os/exec"
	"syscall"
)

// StartBackground starts cmd in its own session so it outlives the caller
// (closing the desktop window or its terminal doesn't take it down).
func StartBackground(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// Reexec replaces the current process with a fresh copy of its executable,
// same arguments, with env. On success it doesn't return.
func Reexec(env []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, env)
}
