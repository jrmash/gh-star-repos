package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
	"go.yaml.in/yaml/v3"

	"github.com/jrmash/gh-star-repos/internal/stars"
)

type Document struct {
	Host              string         `json:"host" yaml:"host"`
	Owner             string         `json:"owner" yaml:"owner"`
	DryRun            bool           `json:"dry_run" yaml:"dry_run"`
	DiscoveryComplete bool           `json:"discovery_complete" yaml:"discovery_complete"`
	LimitReached      bool           `json:"limit_reached" yaml:"limit_reached"`
	ListChanges       []ListChange   `json:"list_changes" yaml:"list_changes"`
	Results           []stars.Result `json:"results" yaml:"results"`
	Summary           Summary        `json:"summary" yaml:"summary"`
	Error             string         `json:"error,omitempty" yaml:"error,omitempty"`
}

type ListChange struct {
	Name       string `json:"name" yaml:"name"`
	ID         string `json:"id,omitempty" yaml:"id,omitempty"`
	Visibility string `json:"visibility" yaml:"visibility"`
	Status     string `json:"status" yaml:"status"`
	Error      string `json:"error,omitempty" yaml:"error,omitempty"`
}

type Summary struct {
	Scanned      int `json:"scanned" yaml:"scanned"`
	Matched      int `json:"matched" yaml:"matched"`
	Selected     int `json:"selected" yaml:"selected"`
	Processed    int `json:"processed" yaml:"processed"`
	Planned      int `json:"planned" yaml:"planned"`
	Starred      int `json:"starred" yaml:"starred"`
	ListUpdates  int `json:"list_updates" yaml:"list_updates"`
	ListsCreated int `json:"lists_created" yaml:"lists_created"`
	Unchanged    int `json:"unchanged" yaml:"unchanged"`
	Failed       int `json:"failed" yaml:"failed"`
}

func (s *Summary) add(result stars.Result) {
	if result.Status == "planned" {
		s.Planned++
		return
	}
	s.Processed++
	if result.Star.Status == "starred" {
		s.Starred++
	}
	if result.Lists.Status == "updated" {
		s.ListUpdates++
	}
	if result.Status == "failed" {
		s.Failed++
	}
}

func encode(out io.Writer, doc any, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return encoder.Encode(doc)
	}
	encoder := yaml.NewEncoder(out)
	encoder.SetIndent(2)
	defer encoder.Close()
	return encoder.Encode(doc)
}

func render(out io.Writer, doc Document, format string, color, terminal bool, width int) error {
	if format != "human" {
		return encode(out, doc, format)
	}
	if !terminal {
		width = 0
	}
	names := make([]string, 0, len(doc.Results)+len(doc.ListChanges))
	for _, result := range doc.Results {
		names = append(names, result.Repository)
	}
	for _, change := range doc.ListChanges {
		names = append(names, "list: "+change.Name)
	}
	nameWidth := humanRepositoryWidth(names, width)
	for _, change := range doc.ListChanges {
		detail := change.Visibility
		if change.Error != "" {
			detail += " · " + change.Error
		}
		if err := renderHumanRow(out, "list: "+change.Name, change.Status, detail, color, terminal, nameWidth, width); err != nil {
			return err
		}
	}
	for _, result := range doc.Results {
		if err := renderHumanResult(out, result, color, terminal, nameWidth, width); err != nil {
			return err
		}
	}
	s := doc.Summary
	if doc.DryRun {
		_, err := fmt.Fprintf(out, "%d selected · %d planned · dry run: no stars or lists changed\n", s.Selected, s.Planned)
		return err
	}
	_, err := fmt.Fprintf(out, "%d selected · %d starred · %d list updates · %d unchanged · %d failed\n", s.Selected, s.Starred, s.ListUpdates, s.Unchanged, s.Failed)
	return err
}

// Keep a fixed status column across the batch, reserving space for the icon,
// separator, longest status, and a spare cell to avoid terminal autowrapping.
func humanRepositoryWidth(names []string, terminalWidth int) int {
	width := 1
	for _, name := range names {
		width = max(width, uniseg.StringWidth(oneLine(name)))
	}
	if terminalWidth > 0 {
		width = min(width, max(1, terminalWidth-13))
	}
	return width
}

func renderHumanResult(out io.Writer, result stars.Result, color, terminal bool, nameWidth, terminalWidth int) error {
	parts := []string{}
	if result.Status == "failed" && result.Star.Status == "starred" {
		parts = append(parts, "star added")
	}
	if result.Lists.Status == "updated" || result.Lists.Status == "planned" {
		parts = append(parts, "lists: "+strings.Join(result.Lists.Added, ", "))
	}
	if result.Star.Error != "" {
		parts = append(parts, "star: "+result.Star.Error)
	}
	if result.Lists.Error != "" {
		parts = append(parts, "lists: "+result.Lists.Error)
	}
	return renderHumanRow(out, result.Repository, result.Status, strings.Join(parts, " · "), color, terminal, nameWidth, terminalWidth)
}

func renderHumanRow(out io.Writer, label, status, detail string, color, terminal bool, nameWidth, terminalWidth int) error {
	symbol, tone := "+", "32"
	if terminal {
		symbol = "✓"
	}
	switch status {
	case "planned":
		symbol, tone = "-", "36"
		if terminal {
			symbol = "→"
		}
	case "failed":
		symbol, tone = "x", "31"
		if terminal {
			symbol = "✗"
		}
	case "not_attempted":
		symbol, tone = "-", "90"
	}
	nameWidth = max(1, nameWidth)
	name := truncateProgress(oneLine(label), nameWidth)
	name += strings.Repeat(" ", max(0, nameWidth-uniseg.StringWidth(name)))
	status = oneLine(strings.ReplaceAll(status, "_", " "))
	line := symbol + " " + name + " " + status
	if detail != "" {
		line += strings.Repeat(" ", max(0, 9-uniseg.StringWidth(status))) + " · " + oneLine(detail)
	}
	if terminal && terminalWidth > 0 {
		line = truncateProgress(line, max(1, terminalWidth-1))
	}
	if color && strings.HasPrefix(line, symbol) {
		line = "\x1b[" + tone + "m" + symbol + "\x1b[0m" + strings.TrimPrefix(line, symbol)
	}
	line += "\n"
	n, err := io.WriteString(out, line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	return err
}

func oneLine(value string) string {
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }), " ")
}
