package executil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	case "tree":
		child := exec.Command(os.Args[0], "-test.run=TestRunnerHelper", "--", "heartbeat", args[2])
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(93)
		}
		_ = child.Wait()
		os.Exit(0)
	case "heartbeat":
		for i := 0; i < 200; i++ {
			if err := os.WriteFile(args[2], []byte(fmt.Sprint(i)), 0600); err != nil {
				os.Exit(94)
			}
			time.Sleep(25 * time.Millisecond)
		}
		os.Exit(0)
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

func TestProcessRunnerCancellationStopsDescendants(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_PATH", binary)
	t.Setenv("GHSR_TEST_HELPER", "1")
	path := filepath.Join(t.TempDir(), "heartbeat")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (ProcessRunner{}).Run(ctx, "gh", "-test.run=TestRunnerHelper", "--", "tree", path)
		done <- err
	}()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.ReadFile(path); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("helper exited before readiness: %v", err)
		case <-deadline:
			t.Fatal("descendant did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("runner did not return promptly")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("descendant still running: before=%q after=%q err=%v", before, after, err)
	}
}
