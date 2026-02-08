package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rivo/uniseg"

	"github.com/jrmash/gh-star-repos/internal/testutil"
)

func humanOptions(t *testing.T, api *testutil.API, out, stderr io.Writer) Options {
	t.Helper()
	cwd := t.TempDir()
	return Options{
		Out: out, Err: stderr, Runner: api,
		Getwd: func() (string, error) { return cwd, nil }, Host: func() string { return "github.com" },
		LookupEnv: func(key string) (string, bool) {
			switch key {
			case "GH_CONFIG_DIR":
				return filepath.Join(cwd, "config"), true
			case "TERM":
				return "xterm-256color", true
			}
			return "", false
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
}

func TestHumanStreamsOneAlignedRowPerRepository(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
		width    int
	}{
		{"redirected", false, 20},
		{"wide terminal", true, 120},
		{"narrow terminal", true, 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testutil.NewAPI(3)
			names := []string{"a", "medium-name", "perl-Dist-Zilla-PluginBundle-Author-JRMASH"}
			for index, name := range names {
				a.Repositories[index].Name = name
				a.Repositories[index].NameWithOwner = "Org/" + name
			}
			var out bytes.Buffer
			transcript := &progressRecorder{}
			writer := progressWriterFunc(func(data []byte) (int, error) {
				// The spinner must be joined and cleared before a permanent row.
				if tc.terminal && !strings.Contains(string(data), "selected ·") && !strings.HasSuffix(transcript.String(), "\r\x1b[2K") {
					t.Errorf("result written over active progress: %q", transcript.String())
				}
				_, _ = transcript.Write(data)
				return out.Write(data)
			})
			o := humanOptions(t, a, writer, transcript)
			o.Terminal, o.Color, o.ProgressTerminal = tc.terminal, tc.terminal, tc.terminal
			o.OutputWidth, o.ProgressWidth = tc.width, tc.width
			a.Fail = func(_ context.Context, call testutil.Call) (string, error, bool) {
				if call.Operation == "GetRepositories" && tc.terminal && !strings.Contains(transcript.String(), "Discovering repositories for org") {
					t.Fatal("discovery started before feedback")
				}
				if call.Operation == "AddStar" && strings.Count(out.String(), "\n") != a.Count("AddStar")-1 {
					t.Fatal("previous result was not streamed before the next mutation", out.String())
				}
				return "", nil, false
			}
			if code := Execute(context.Background(), []string{"org", "--sleep=0"}, o); code != 0 {
				t.Fatal(code, transcript.String())
			}
			rows := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
			if len(rows) != 4 || strings.Count(transcript.String(), "\n") != 4 {
				t.Fatalf("expected one permanent line per repository and a summary: %q", transcript.String())
			}
			column := 3 + len("Org/"+names[2])
			if tc.terminal && tc.width == 40 {
				column = 30
			}
			for _, row := range rows[:3] {
				if humanStatusColumn(t, row, "starred") != column {
					t.Errorf("incorrect status column: %q", row)
				}
				plain := humanColorEscape.ReplaceAllString(row, "")
				if tc.terminal && uniseg.StringWidth(plain) >= tc.width {
					t.Errorf("result wraps terminal: %q", row)
				}
			}
			if !tc.terminal && (strings.Contains(transcript.String(), "\x1b") || !strings.Contains(out.String(), "Org/"+names[2])) {
				t.Fatalf("redirected output lost the full name or contains controls: %q", transcript.String())
			}
		})
	}
}

func TestHumanSpinnerShowsListAndRetryPhasesWithoutExtraLines(t *testing.T) {
	a := testutil.NewAPI(2)
	a.Owner = a.Viewer
	for index := range a.Repositories {
		a.Repositories[index].Owner.Login = a.Owner
		a.Repositories[index].NameWithOwner = a.Owner + "/" + a.Repositories[index].Name
	}
	a.Fail = func(_ context.Context, call testutil.Call) (string, error, bool) {
		if call.Operation == "AddStar" && a.Count("AddStar") == 1 {
			output, err := testutil.Failure(503, "temporarily unavailable", "")
			return output, err, true
		}
		return "", nil, false
	}
	var out bytes.Buffer
	progress := &progressRecorder{}
	o := humanOptions(t, a, &out, progress)
	o.Terminal, o.ProgressTerminal, o.OutputWidth, o.ProgressWidth = true, true, 120, 120
	if code := Execute(context.Background(), []string{"--list", "Go", "--create-lists", "--sleep=0"}, o); code != 0 {
		t.Fatal(code, progress.String())
	}
	for _, phase := range []string{"Resolving authenticated user", "Reading your star lists", "Discovering repositories", "Creating star list Go", "Updating me/repo-0000", "Waiting 1s: retrying GitHub request"} {
		if !strings.Contains(progress.String(), phase) {
			t.Errorf("missing phase %q in %q", phase, progress.String())
		}
	}
	if strings.Contains(progress.String(), "\n") || !strings.HasSuffix(progress.String(), "\r\x1b[2K") || strings.Count(out.String(), "\n") != 4 || strings.Count(out.String(), "lists: Go") != 2 {
		t.Fatalf("permanent progress or extra repository rows: stdout=%q stderr=%q", out.String(), progress.String())
	}
}

func TestStructuredAndDumbOutputSuppressProgress(t *testing.T) {
	for _, format := range []string{"json", "yaml", "human"} {
		t.Run(format, func(t *testing.T) {
			a := testutil.NewAPI(2)
			var out, stderr bytes.Buffer
			o := humanOptions(t, a, &out, &stderr)
			o.ProgressTerminal = true
			lookup := o.LookupEnv
			o.LookupEnv = func(key string) (string, bool) {
				if key == "TERM" && format == "human" {
					return "dumb", true
				}
				return lookup(key)
			}
			if code := Execute(context.Background(), []string{"org", "--dry-run", "--format=" + format}, o); code != 0 {
				t.Fatal(code, stderr.String())
			}
			if stderr.Len() != 0 || strings.Contains(out.String(), "\x1b") || a.Mutations() != 0 {
				t.Fatalf("unexpected progress, styling, or mutations: stdout=%q stderr=%q", out.String(), stderr.String())
			}
		})
	}
}

func TestHumanProgressFailuresStopMutations(t *testing.T) {
	for _, phase := range []string{"first frame", "clear discovery", "second repository"} {
		t.Run(phase, func(t *testing.T) {
			a := testutil.NewAPI(3)
			var out bytes.Buffer
			stderr := progressWriterFunc(func(data []byte) (int, error) {
				if phase == "first frame" || phase == "clear discovery" && string(data) == "\r\x1b[2K" || phase == "second repository" && strings.Contains(string(data), "Updating Org/repo-0001") {
					return 0, fmt.Errorf("terminal unavailable")
				}
				return len(data), nil
			})
			o := humanOptions(t, a, &out, stderr)
			o.ProgressTerminal = true
			code := Execute(context.Background(), []string{"org", "--sleep=0"}, o)
			wantWrites := 0
			if phase == "second repository" {
				wantWrites = 1
			}
			if code != 1 || a.Count("AddStar") != wantWrites {
				t.Fatalf("progress failure allowed further mutations: code=%d writes=%d stdout=%q", code, a.Count("AddStar"), out.String())
			}
		})
	}
}

func TestHumanProgressClearsAfterInterruption(t *testing.T) {
	a := testutil.NewAPI(3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Fail = func(_ context.Context, call testutil.Call) (string, error, bool) {
		if call.Operation == "AddStar" {
			cancel()
			return "", context.Canceled, true
		}
		return "", nil, false
	}
	var out bytes.Buffer
	progress := &progressRecorder{}
	o := humanOptions(t, a, &out, progress)
	o.ProgressTerminal = true
	if code := Execute(ctx, []string{"org", "--sleep=0"}, o); code != 130 || a.Count("AddStar") != 1 {
		t.Fatal(code, a.Count("AddStar"), progress.String())
	}
	if !strings.Contains(progress.String(), "\r\x1b[2Kerror:") || strings.Count(out.String(), "Org/repo-0000") != 1 || !strings.Contains(out.String(), "failed") {
		t.Fatalf("interruption left active progress or lost result: stdout=%q stderr=%q", out.String(), progress.String())
	}
}

func TestDefaultTerminalOptionsRespectColorPreferences(t *testing.T) {
	t.Setenv("GH_FORCE_TTY", "42")
	t.Setenv("CLICOLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "0")
	for _, noColor := range []string{"", "1"} {
		t.Setenv("NO_COLOR", noColor)
		o := defaults(Options{})
		if !o.Terminal || o.OutputWidth != 42 || o.Color != (noColor == "") || o.Out == nil || o.Err == nil {
			t.Fatalf("terminal defaults ignored preferences: %+v", o)
		}
	}
	o := defaults(Options{Out: io.Discard, Err: io.Discard, Terminal: true})
	if o.OutputWidth != 80 {
		t.Fatalf("terminal width fallback = %d", o.OutputWidth)
	}
}
