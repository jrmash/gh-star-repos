//go:build smoke

// Package integration exercises a locally installed extension through real gh.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jrmash/gh-star-repos/internal/cli"
)

func TestInstalledExtension(t *testing.T) {
	for key, actual := range map[string]string{"GHSR_SMOKE_GOOS": runtime.GOOS, "GHSR_SMOKE_GOARCH": runtime.GOARCH} {
		if expected := os.Getenv(key); expected != "" && expected != actual {
			t.Fatalf("%s requires %s; running natively as %s", key, expected, actual)
		}
	}
	t.Logf("Native smoke test: %s/%s", runtime.GOOS, runtime.GOARCH)
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal("smoke tests require GitHub CLI on PATH")
	}
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		cmd := exec.Command(gh, "auth", "token", "--hostname", "github.com")
		output, err := cmd.Output()
		if err != nil {
			t.Fatal("smoke tests require GH_TOKEN, GITHUB_TOKEN, or existing gh authentication")
		}
		token = strings.TrimSpace(string(output))
	}
	if token == "" {
		t.Fatal("smoke tests require a GitHub token")
	}

	root := t.TempDir()
	installation, working := filepath.Join(root, "gh-star-repos"), filepath.Join(root, "working")
	for _, directory := range []string{installation, working} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(installation, "gh-star-repos")
	if runtime.GOOS == "windows" {
		binary += ".exe"
		// Local Windows extensions are dispatched through sh by GitHub CLI.
		quoted := "'" + strings.ReplaceAll(filepath.ToSlash(binary), "'", "'\\''") + "'"
		launcher := "#!/bin/sh\nexec " + quoted + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(installation, "gh-star-repos"), []byte(launcher), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	command(t, "..", os.Environ(), "go", "build", "-buildvcs=false", "-ldflags=-X main.version=smoke", "-o", binary, ".")
	env := isolatedEnvironment(root, token)
	command(t, installation, env, gh, "extension", "install", ".")
	if got := strings.TrimSpace(command(t, working, env, gh, "star-repos", "--version")); got != "gh star-repos smoke" {
		t.Fatalf("installed version = %q", got)
	}
	help := command(t, working, env, gh, "star-repos", "--help")
	for _, flag := range []string{"--dry-run", "--max-repos", "--list", "--create-lists", "--format", "--exclude"} {
		if !strings.Contains(help, flag) {
			t.Fatalf("installed extension help omits %s", flag)
		}
	}
	var config struct {
		Settings map[string]any    `json:"settings"`
		Sources  map[string]string `json:"sources"`
	}
	output := command(t, working, env, gh, "star-repos", "config", "show", "--sources", "--format=json")
	if err := json.Unmarshal([]byte(output), &config); err != nil || config.Settings["forks"] != "exclude" || config.Sources["forks"] != "default" {
		t.Fatalf("installed configuration output: %s (%v)", output, err)
	}

	args := []string{"star-repos", "octocat", "--archived=any", "--forks=any", "--visibility=public", "--max-repos=1", "--dry-run", "--format=json"}
	for _, excluded := range []bool{false, true} {
		call := append([]string{}, args...)
		if excluded {
			call = append(call, "--exclude=*")
		}
		output = command(t, working, env, gh, call...)
		var doc cli.Document
		if err := json.Unmarshal([]byte(output), &doc); err != nil {
			t.Fatalf("installed dry run did not produce JSON: %v\n%s", err, output)
		}
		if !doc.DryRun || doc.Owner != "octocat" || doc.Error != "" || doc.Summary.Selected > 1 || doc.Summary.Planned != len(doc.Results) || doc.Summary.Processed != 0 || doc.Summary.Starred+doc.Summary.ListUpdates+doc.Summary.ListsCreated+doc.Summary.Failed != 0 || doc.Summary.Scanned == 0 {
			t.Fatalf("unexpected installed dry run: %+v", doc)
		}
		if excluded && (doc.Summary.Selected != 0 || doc.Summary.Matched != 0 || !doc.DiscoveryComplete) {
			t.Fatalf("exclusions did not apply to the installed extension: %+v", doc)
		}
		if !excluded && doc.Summary.Matched == 0 {
			t.Fatal("public fixture returned no matching repositories")
		}
		for _, result := range doc.Results {
			if result.Status != "planned" || result.Star.Status != "planned" || result.Lists.Status != "unchanged" || !strings.HasPrefix(result.Repository, "octocat/") {
				t.Fatalf("unexpected planned action: %+v", result)
			}
		}
	}
	command(t, working, env, gh, "extension", "remove", "star-repos")
}

func command(t *testing.T, cwd string, env []string, executable string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir, cmd.Env = cwd, env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s failed: %v\nstdout:\n%s\nstderr:\n%s", filepath.Base(executable), strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func isolatedEnvironment(root, token string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if strings.HasPrefix(key, "GH_") || strings.HasPrefix(key, "GHSR_") || strings.HasPrefix(key, "GIT_") || key == "GITHUB_TOKEN" || strings.HasPrefix(key, "XDG_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"GH_TOKEN="+token, "GH_HOST=github.com", "GH_CONFIG_DIR="+filepath.Join(root, "config"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "xdg-config"), "XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"), "XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_NO_EXTENSION_UPDATE_NOTIFIER=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "empty-git-config"), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "NO_COLOR=1",
	)
}
