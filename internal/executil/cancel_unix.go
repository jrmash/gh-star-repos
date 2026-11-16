//go:build !windows

package executil

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureCancellation(cmd *exec.Cmd) {
	// Descendants inherit this group, including credential helpers that keep
	// stdout or stderr open after the CLI exits.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
