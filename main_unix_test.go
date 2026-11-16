//go:build !windows

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// Run the real entry point with this binary standing in for gh and its helper.
// No credentials or network are needed; the request blocks until signaled.
func TestMain(m *testing.M) {
	if os.Getenv("GHSR_SIGNAL_TEST") != "1" {
		os.Exit(m.Run())
	}
	path := os.Getenv("GHSR_SIGNAL_HEARTBEAT")
	switch os.Args[1] {
	case "api":
		if err := os.WriteFile(path+".ghpid", []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(90)
		}
		child := exec.Command(os.Args[0], "signal-test-leaf")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Run(); err != nil {
			os.Exit(91)
		}
		os.Exit(0)
	case "signal-test-leaf":
		// Bound the helper lifetime even if a broken cancellation path leaves
		// it behind and the parent test unexpectedly exits.
		for i := 0; i < 400; i++ {
			if err := os.WriteFile(path, []byte(strconv.Itoa(i)), 0600); err != nil {
				os.Exit(92)
			}
			time.Sleep(25 * time.Millisecond)
		}
		os.Exit(0)
	default:
		main()
	}
}

func TestTerminationCleansUpCLIProcessGroup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signal syscall.Signal
		group  bool
	}{
		{"SIGTERM to extension", syscall.SIGTERM, false},
		{"SIGTERM to job group", syscall.SIGTERM, true},
		{"interrupt", syscall.SIGINT, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "heartbeat")
			t.Setenv("GHSR_SIGNAL_TEST", "1")
			t.Setenv("GHSR_SIGNAL_HEARTBEAT", path)
			t.Setenv("GH_PATH", binary)
			t.Setenv("GH_CONFIG_DIR", filepath.Join(dir, "config"))
			cmd := exec.Command(binary, "signal-test-owner", "--dry-run", "--format", "json", "--sleep", "0")
			cmd.Dir = dir
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.WaitDelay = 2 * time.Second
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				// gh has its own group. Clean that up on assertion failures too.
				if data, err := os.ReadFile(path + ".ghpid"); err == nil {
					if pid, err := strconv.Atoi(string(data)); err == nil && pid > 0 {
						_ = syscall.Kill(-pid, syscall.SIGKILL)
					}
				}
			})
			deadline := time.After(5 * time.Second)
			for {
				if _, err := os.ReadFile(path); err == nil {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("extension exited before helper was ready: %v\n%s", err, &output)
				case <-deadline:
					t.Fatal("helper did not become ready")
				case <-time.After(10 * time.Millisecond):
				}
			}
			pid := cmd.Process.Pid
			if tc.group {
				pid = -pid
			}
			if err := syscall.Kill(pid, tc.signal); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 130 {
					t.Fatalf("expected cancellation exit 130, got %v\n%s", err, &output)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("extension did not finish cleanup promptly")
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(150 * time.Millisecond)
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("helper survived extension exit: before=%q after=%q err=%v", before, after, err)
			}
		})
	}
}
