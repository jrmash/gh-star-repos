package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"go.yaml.in/yaml/v3"

	"github.com/jrmash/gh-star-repos/internal/stars"
)

var humanColorEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func humanStatusColumn(t *testing.T, line, status string) int {
	t.Helper()
	plain := humanColorEscape.ReplaceAllString(line, "")
	index := strings.LastIndex(plain, status)
	if index < 0 {
		t.Fatalf("status %q missing from %q", status, line)
	}
	return uniseg.StringWidth(plain[:index])
}

func TestHumanOutputAlignedRowsWithInlineDetails(t *testing.T) {
	doc := Document{Results: []stars.Result{
		{Repository: "octo/a", Status: "starred"},
		{Repository: "octo/longer", Status: "listed", Lists: stars.Action{Status: "updated", Added: []string{"Go", "CLI"}}},
		{Repository: "octo/fail", Status: "failed", Star: stars.Action{Status: "starred"}, Lists: stars.Action{Status: "failed", Error: "permission denied\nplease retry"}},
	}, Summary: Summary{Selected: 3, Processed: 3, Starred: 2, ListUpdates: 1, Failed: 1}}
	var out bytes.Buffer
	if err := render(&out, doc, "human", false, false, 20); err != nil {
		t.Fatal(err)
	}
	want := "+ octo/a      starred\n" +
		"+ octo/longer listed    · lists: Go, CLI\n" +
		"x octo/fail   failed    · star added · lists: permission denied please retry\n" +
		"3 selected · 2 starred · 1 list updates · 0 unchanged · 1 failed\n"
	if out.String() != want {
		t.Fatalf("human table:\n%q\nwant:\n%q", out.String(), want)
	}
}

func TestHumanColumnsAndStatusSymbols(t *testing.T) {
	names := []string{
		"jrmash/bfl",
		"jrmash/perl-Dist-Zilla-Plugin-PkgHeader",
		"jrmash/perl-Dist-Zilla-PluginBundle-Author-JRMASH",
		"jrmash/terramate-component-library",
	}
	statuses := []string{"starred", "listed", "planned", "failed"}
	for _, terminal := range []bool{false, true} {
		for _, color := range []bool{false, true} {
			width := humanRepositoryWidth(names, 120)
			for index, name := range names {
				var out bytes.Buffer
				if err := renderHumanResult(&out, stars.Result{Repository: name, Status: statuses[index]}, color, terminal, width, 120); err != nil {
					t.Fatal(err)
				}
				line := out.String()
				if column := humanStatusColumn(t, line, statuses[index]); column != 52 || !strings.Contains(line, name) {
					t.Errorf("terminal=%t color=%t: column %d/full name in %q", terminal, color, column, line)
				}
				symbol := []string{"+", "+", "-", "x"}[index]
				if terminal {
					symbol = []string{"✓", "✓", "→", "✗"}[index]
				}
				plain := humanColorEscape.ReplaceAllString(line, "")
				if !strings.HasPrefix(plain, symbol+" ") || strings.Contains(line, "\x1b") != color {
					t.Errorf("status styling: %q", line)
				}
			}
		}
	}
}

func TestHumanRowsFitNarrowTerminals(t *testing.T) {
	names := []string{"octo/a", "jrmash/perl-Dist-Zilla-PluginBundle-Author-JRMASH"}
	for _, terminalWidth := range []int{1, 20, 32, 60} {
		nameWidth := humanRepositoryWidth(names, terminalWidth)
		for _, name := range names {
			var out bytes.Buffer
			result := stars.Result{Repository: name, Status: "failed", Star: stars.Action{Error: "a very long error\nwith another line and 👩‍💻 emoji"}}
			if err := renderHumanResult(&out, result, true, true, nameWidth, terminalWidth); err != nil {
				t.Fatal(err)
			}
			line := humanColorEscape.ReplaceAllString(strings.TrimSuffix(out.String(), "\n"), "")
			if cells := uniseg.StringWidth(line); cells > max(1, terminalWidth-1) || !utf8.ValidString(line) || strings.ContainsAny(line, "\r\n") {
				t.Errorf("width %d: %q (%d cells)", terminalWidth, line, cells)
			}
			if terminalWidth >= 20 && humanStatusColumn(t, line, "failed") != nameWidth+3 {
				t.Errorf("misaligned status: %q", line)
			}
		}
	}
}

func TestHumanWidthAndUnicodeNames(t *testing.T) {
	for _, tc := range []struct {
		names []string
		limit int
		want  int
	}{
		{nil, 0, 1},
		{[]string{"", " \t\n"}, 0, 1},
		{[]string{" \tocto/a\n"}, 0, 6},
		{[]string{"octo/日"}, 0, 7},
		{[]string{"octo/e\u0301"}, 0, 6},
		{[]string{"octo/👩‍💻"}, 0, 7},
		{[]string{"octo/very-long-repository"}, 20, 7},
		{[]string{"octo/very-long-repository"}, 1, 1},
		{[]string{"octo/a"}, -1, 6},
	} {
		if got := humanRepositoryWidth(tc.names, tc.limit); got != tc.want {
			t.Errorf("width(%q, %d) = %d, want %d", tc.names, tc.limit, got, tc.want)
		}
	}
	names := []string{"octo/a", "octo/日日本", "octo/e\u0301clair", "octo/👩‍💻-tools"}
	width := humanRepositoryWidth(names, 0)
	for _, name := range names {
		var out bytes.Buffer
		if err := renderHumanResult(&out, stars.Result{Repository: name, Status: "starred"}, false, false, width, 0); err != nil {
			t.Fatal(err)
		}
		if column := humanStatusColumn(t, out.String(), "starred"); column != width+3 || !strings.Contains(out.String(), name) {
			t.Errorf("Unicode alignment/full name: %q", out.String())
		}
	}
	for _, tc := range []struct {
		width int
		want  string
	}{
		{8, "octo/e\u0301…"},
		{9, "octo/e\u0301👩‍💻…"},
	} {
		var out bytes.Buffer
		if err := renderHumanResult(&out, stars.Result{Repository: "octo/e\u0301👩‍💻-日本", Status: "starred"}, false, false, tc.width, 0); err != nil {
			t.Fatal(err)
		}
		line := out.String()
		end := strings.LastIndex(line, " starred")
		if end < 2 || strings.TrimSpace(line[2:end]) != tc.want || humanStatusColumn(t, line, "starred") != tc.width+3 {
			t.Errorf("Unicode truncation = %q, want %q", line, tc.want)
		}
	}
}

func TestHumanListChangesAndDryRun(t *testing.T) {
	doc := Document{DryRun: true, ListChanges: []ListChange{
		{Name: "Go\nCLI", Status: "planned", Visibility: "private"},
		{Name: "Created", Status: "created", Visibility: "public"},
		{Name: "Failed", Status: "failed", Visibility: "private", Error: "denied\r\nretry"},
		{Name: "Later", Status: "not_attempted", Visibility: "private"},
	}, Results: []stars.Result{{Repository: "octo/repo", Status: "planned", Lists: stars.Action{Status: "planned", Added: []string{"Go"}}}}, Summary: Summary{Selected: 1, Planned: 1}}
	var out bytes.Buffer
	if err := render(&out, doc, "human", false, false, 0); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	for index, snippet := range []string{"- list: Go CLI", "+ list: Created", "x list: Failed", "- list: Later", "- octo/repo", "1 selected · 1 planned · dry run"} {
		if len(lines) != 6 || !strings.HasPrefix(lines[index], snippet) {
			t.Fatalf("unexpected list output: %q", out.String())
		}
	}
	if !strings.Contains(out.String(), "denied retry") || !strings.Contains(out.String(), "not attempted") || !strings.Contains(out.String(), "lists: Go") {
		t.Fatalf("missing inline details: %q", out.String())
	}
}

func TestStructuredOutputPreservesFullDetails(t *testing.T) {
	const repository = "jrmash/perl-Dist-Zilla-PluginBundle-Author-JRMASH"
	doc := Document{Results: []stars.Result{{Repository: repository, Status: "failed", Lists: stars.Action{Status: "failed", Error: "first\nsecond"}}}}
	for _, format := range []string{"json", "yaml"} {
		var out bytes.Buffer
		if err := render(&out, doc, format, true, true, 20); err != nil {
			t.Fatal(err)
		}
		var decoded Document
		var err error
		if format == "json" {
			err = json.Unmarshal(out.Bytes(), &decoded)
		} else {
			err = yaml.Unmarshal(out.Bytes(), &decoded)
		}
		if err != nil || !reflect.DeepEqual(decoded.Results, doc.Results) {
			t.Errorf("%s details changed: %v %s", format, err, out.String())
		}
		if strings.Contains(out.String(), "\x1b") {
			t.Errorf("%s contains terminal controls: %q", format, out.String())
		}
	}
}

func TestHumanOutputWriteFailures(t *testing.T) {
	for _, doc := range []Document{
		{ListChanges: []ListChange{{Name: "Go", Status: "created"}}},
		{Results: []stars.Result{{Repository: "octo/a", Status: "starred"}}},
		{DryRun: true},
		{},
	} {
		if err := render(brokenWriter{}, doc, "human", false, false, 0); err == nil {
			t.Fatalf("output error ignored for %+v", doc)
		}
	}
	short := progressWriterFunc(func(data []byte) (int, error) { return len(data) - 1, nil })
	if err := renderHumanResult(short, stars.Result{Repository: "octo/a"}, false, false, 6, 0); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short row write: %v", err)
	}
}
