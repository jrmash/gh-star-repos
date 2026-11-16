package executil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

func configureCancellation(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		// taskkill's tree mode includes helpers spawned by gh. Use the system
		// executable rather than resolving it through PATH.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		kill := exec.CommandContext(ctx, filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe"), "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		if err := kill.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
