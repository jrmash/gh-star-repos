package executil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Execute this test binary as a stand-in for gh, without a shell or network.
func TestRunnerHelper(t *testing.T) {
	if os.Getenv("GHSR_TEST_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(90)
	}
	if os.Getenv("GH_PROMPT_DISABLED") != "1" || os.Getenv("GH_FORCE_TTY") != "" || os.Getenv("GIT_TERMINAL_PROMPT") != "0" || os.Getenv("GIT_DIR") != "" {
		os.Exit(91)
	}
	switch args[1] {
	case "success":
		fmt.Print(strings.Join(args[2:], "|"))
		os.Exit(0)
	case "failure":
		fmt.Print("partial response")
		fmt.Fprintln(os.Stderr, "permission denied")
		os.Exit(2)
	case "wait":
		time.Sleep(time.Minute)
		os.Exit(0)
	default:
		os.Exit(92)
	}
}

func TestProcessRunnerPreservesArgumentsAndErrorOutput(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_PATH", binary)
	t.Setenv("GHSR_TEST_HELPER", "1")
	t.Setenv("GH_FORCE_TTY", "1")
	t.Setenv("GIT_DIR", "unrelated-repository")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	args := []string{"-test.run=TestRunnerHelper", "--", "success", "space in name", "$(literal)", "`literal`", "--query=value"}
	output, err := (ProcessRunner{}).Run(context.Background(), "gh", args...)
	if err != nil || output != "space in name|$(literal)|`literal`|--query=value" {
		t.Fatalf("arguments were changed or evaluated: %q %v", output, err)
	}
	output, err = (ProcessRunner{}).Run(context.Background(), "gh", "-test.run=TestRunnerHelper", "--", "failure")
	if output != "partial response" || err == nil || err.Error() != "gh: permission denied" {
		t.Fatalf("lost partial response/stderr: %q %v", output, err)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("lost process exit status: %v", err)
	}
}

func TestProcessRunnerCancellation(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_PATH", binary)
	t.Setenv("GHSR_TEST_HELPER", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = (ProcessRunner{}).Run(ctx, "gh", "-test.run=TestRunnerHelper", "--", "wait")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("process did not honor cancellation: %v", err)
	}
}
