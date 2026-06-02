// Package executil invokes GitHub CLI without a shell.
package executil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	gh "github.com/cli/go-gh/v2"
	"github.com/cli/safeexec"
)

// Runner is injectable so tests never need GitHub credentials or real stars.
type Runner interface {
	Run(context.Context, string, ...string) (string, error)
}

type ProcessRunner struct{}

type commandError struct {
	name    string
	message string
	cause   error
}

func (e *commandError) Error() string { return e.name + ": " + e.message }
func (e *commandError) Unwrap() error { return e.cause }

func (ProcessRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var executable string
	var err error
	if name == "gh" {
		executable, err = gh.Path()
	} else {
		executable, err = safeexec.LookPath(name)
	}
	if err != nil {
		return "", fmt.Errorf("find %s: %w", name, err)
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = commandEnv(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return stdout.String(), ctx.Err()
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return stdout.String(), &commandError{name: name, message: message, cause: err}
	}
	return stdout.String(), nil
}

func commandEnv(env []string) []string {
	result := make([]string, 0, len(env)+4)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "GH_PROMPT_DISABLED", "GH_FORCE_TTY", "GH_PAGER", "PAGER", "CLICOLOR_FORCE":
			continue
		case "GIT_TERMINAL_PROMPT", "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR":
			continue
		}
		result = append(result, entry)
	}
	return append(result, "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "GH_PAGER=cat", "CLICOLOR_FORCE=0")
}
