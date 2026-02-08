package executil

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	if os.Getenv("GH_PROMPT_DISABLED") != "1" || os.Getenv("GH_FORCE_TTY") != "" {
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
	args := []string{"-test.run=TestRunnerHelper", "--", "success", "space in name", "$(literal)", "`literal`", "--query=value"}
	output, err := (ProcessRunner{}).Run(context.Background(), "gh", args...)
	if err != nil || output != "space in name|$(literal)|`literal`|--query=value" {
		t.Fatalf("arguments were changed or evaluated: %q %v", output, err)
	}
	output, err = (ProcessRunner{}).Run(context.Background(), "gh", "-test.run=TestRunnerHelper", "--", "failure")
	if output != "partial response" || err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("lost partial response/stderr: %q %v", output, err)
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
