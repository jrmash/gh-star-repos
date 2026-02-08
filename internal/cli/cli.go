// Package cli implements the extension's command and batch workflow.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/jrmash/gh-star-repos/internal/executil"
	"github.com/jrmash/gh-star-repos/internal/github"
	"github.com/jrmash/gh-star-repos/internal/settings"
	"github.com/jrmash/gh-star-repos/internal/stars"
)

// Options substitutes external commands and process state in tests, as in
// gh-get-repos. No real account access is required to exercise the entire CLI.
type Options struct {
	Version          string
	Out, Err         io.Writer
	Color, Terminal  bool
	OutputWidth      int
	ProgressTerminal bool
	ProgressWidth    int
	Runner           executil.Runner
	Getwd            func() (string, error)
	LookupEnv        func(string) (string, bool)
	Host             func() string
	Now              func() time.Time
	Sleep            func(context.Context, time.Duration) error
}

type commandError struct {
	code int
	err  error
}

func (e *commandError) Error() string { return e.err.Error() }

// Execute returns the extension's documented exit code without exiting.
func Execute(ctx context.Context, args []string, o Options) int {
	o = defaults(o)
	cmd := newCommand(o)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(ctx); err != nil {
		code := 2
		var failure *commandError
		if errors.As(err, &failure) {
			code = failure.code
		}
		if ctx.Err() != nil {
			code = 130
		}
		fmt.Fprintf(o.Err, "error: %s\n", oneLine(err.Error()))
		return code
	}
	return 0
}

func defaults(o Options) Options {
	terminal := term.FromEnv()
	if o.Out == nil {
		o.Out = terminal.Out()
		o.Color = terminal.IsColorEnabled()
		o.Terminal = terminal.IsTerminalOutput()
		if o.Terminal {
			o.OutputWidth, _, _ = terminal.Size()
		}
	}
	if o.Err == nil {
		o.Err = terminal.ErrOut()
		o.ProgressTerminal = term.IsTerminal(os.Stderr)
		if o.ProgressTerminal {
			o.ProgressWidth, _, _ = terminal.Size()
		}
	}
	if o.Terminal && o.OutputWidth <= 0 {
		o.OutputWidth = 80
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	if o.Runner == nil {
		o.Runner = executil.ProcessRunner{}
	}
	if o.Getwd == nil {
		o.Getwd = os.Getwd
	}
	if o.LookupEnv == nil {
		o.LookupEnv = os.LookupEnv
	}
	if o.Host == nil {
		o.Host = func() string { host, _ := auth.DefaultHost(); return host }
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = github.Sleep
	}
	return o
}

func newCommand(o Options) *cobra.Command {
	cmd := &cobra.Command{
		Use: "star-repos [owner]", Short: "Star repositories owned by a GitHub user or organization",
		Annotations: map[string]string{cobra.CommandDisplayNameAnnotation: "gh star-repos"},
		Long: "Star accessible owned repositories, newest first, and optionally add them to your star lists.\n\n" +
			"Configuration: flags > GHSR_* > ./.gh-star-repos.yml > <gh-config-dir>/star-repos.yml > defaults.\n" +
			"Owner falls back to the authenticated user. Forks are excluded by default.\n" +
			"--max-repos counts repositories needing a star or requested list assignment.",
		Example: "  gh star-repos example-org --dry-run\n  gh star-repos example-org --list Go --create-lists -m 10\n  gh star-repos config show --sources",
		Args:    cobra.MaximumNArgs(1), Version: o.Version, SilenceUsage: true, SilenceErrors: true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	cmd.SetOut(o.Out)
	cmd.SetErr(o.Err)
	cmd.SetVersionTemplate("gh star-repos {{.Version}}\n")
	flags := cmd.PersistentFlags()
	flags.StringP("archived", "a", "any", "Archived repositories: any|exclude|only")
	flags.String("forks", "exclude", "Forked repositories: any|exclude|only")
	flags.StringP("visibility", "v", "any", "Repository visibility: any|public|private|internal")
	flags.StringP("language", "l", "", "Primary language (case insensitive)")
	flags.StringSliceP("topic", "t", nil, "Required topics (CSV or repeat; all must match)")
	flags.StringArray("exclude", nil, "Repository-name glob to exclude (repeatable, case insensitive)")
	flags.StringArray("list", nil, "Exact name of one of your star lists (repeatable)")
	flags.Bool("create-lists", false, "Create missing requested lists before starring")
	flags.String("list-visibility", "private", "Visibility of new lists: private|public")
	flags.IntP("sleep", "s", 1, "Seconds between mutation attempts (0 disables pacing)")
	flags.IntP("max-repos", "m", 0, "Maximum repositories needing changes (0 means unlimited)")
	flags.Bool("dry-run", false, "Preview stars and list changes without mutations")
	flags.String("format", "human", "Output format: human|json|yaml")
	cmd.Flags().BoolP("help", "h", false, "Show help")
	cmd.Flags().Bool("version", false, "Show extension version")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig(cmd, args, o)
		if err != nil {
			return err
		}
		doc, code := run(cmd.Context(), cfg, o)
		display := doc
		if cfg.Format == "human" {
			display.Results = nil // Each repository was already printed as it finished.
		}
		if err := render(o.Out, display, cfg.Format, o.Color, o.Terminal, o.OutputWidth); err != nil {
			return &commandError{1, fmt.Errorf("write results: %w", err)}
		}
		if code != 0 {
			message := doc.Error
			if message == "" {
				message = "one or more repository operations failed"
			}
			return &commandError{code, errors.New(message)}
		}
		return nil
	}
	configCmd := &cobra.Command{Use: "config", Short: "Inspect effective configuration"}
	show := &cobra.Command{Use: "show", Short: "Show settings (empty owner means authenticated user)", Args: cobra.NoArgs}
	show.Flags().Bool("sources", false, "Include the origin of each effective setting")
	show.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := loadConfig(cmd, nil, o)
		if err != nil {
			return err
		}
		sources, _ := cmd.Flags().GetBool("sources")
		var doc any = cfg
		if sources {
			doc = struct {
				Settings settings.Config   `json:"settings" yaml:"settings"`
				Sources  map[string]string `json:"sources" yaml:"sources"`
			}{cfg, cfg.Sources}
		}
		if err := encode(o.Out, doc, cfg.Format); err != nil {
			return &commandError{1, fmt.Errorf("write configuration: %w", err)}
		}
		return nil
	}
	configCmd.AddCommand(show)
	cmd.AddCommand(configCmd)
	return cmd
}

func loadConfig(cmd *cobra.Command, args []string, o Options) (settings.Config, error) {
	cwd, err := o.Getwd()
	if err != nil {
		return settings.Config{}, &commandError{1, fmt.Errorf("get invocation directory: %w", err)}
	}
	overrides := map[string]string{}
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		switch flag.Name {
		case "help", "version", "sources":
		case "topic":
			items, _ := cmd.Flags().GetStringSlice(flag.Name)
			overrides[flag.Name] = settings.EncodeList(items)
		case "list", "exclude":
			items, _ := cmd.Flags().GetStringArray(flag.Name)
			overrides[flag.Name] = settings.EncodeList(items)
		default:
			overrides[flag.Name] = flag.Value.String()
		}
	})
	if len(args) == 1 {
		overrides["owner"] = args[0]
	}
	return settings.Load(cwd, o.LookupEnv, overrides)
}

func run(ctx context.Context, cfg settings.Config, o Options) (Document, int) {
	doc := Document{Owner: cfg.Owner, Host: o.Host(), DryRun: cfg.DryRun, Results: []stars.Result{}, ListChanges: []ListChange{}}
	terminalType, _ := o.LookupEnv("TERM")
	activity := newProgress(o.Err, cfg.Format == "human" && o.ProgressTerminal && terminalType != "dumb", true, o.Color, o.ProgressWidth)
	defer activity.Stop()
	fail := func(err error) (Document, int) {
		if stopErr := activity.Stop(); stopErr != nil && !errors.Is(err, stopErr) {
			err = errors.Join(err, fmt.Errorf("write progress: %w", stopErr))
		}
		doc.Error = err.Error()
		if ctx.Err() != nil {
			return doc, 130
		}
		return doc, 1
	}
	if doc.Host == "" || strings.HasPrefix(doc.Host, "-") || strings.ContainsAny(doc.Host, "/\\:?#[\x00\t\r\n ") {
		return fail(errors.New("invalid GitHub hostname"))
	}
	start := func(message string) error {
		if err := activity.Start(ctx, message); err != nil {
			return fmt.Errorf("write progress: %w", err)
		}
		return nil
	}
	client := &github.Client{Runner: o.Runner, Host: doc.Host, Pause: time.Duration(cfg.Sleep) * time.Second, Now: o.Now, Sleep: o.Sleep,
		OnWait: func(delay time.Duration, reason string) {
			if reason != "between mutations" {
				activity.Update(fmt.Sprintf("Waiting %s: %s", delay.Round(time.Millisecond), reason))
			}
		}}
	if cfg.Owner == "" {
		if err := start("Resolving authenticated user"); err != nil {
			return fail(err)
		}
		owner, err := client.Viewer(ctx)
		if err != nil {
			return fail(err)
		}
		cfg.Owner, doc.Owner = owner, owner
	}
	var snapshot github.Snapshot
	requested, missing := map[string]string{}, []string{}
	if len(cfg.Lists) > 0 {
		if err := start("Reading your star lists and memberships"); err != nil {
			return fail(err)
		}
		lists, err := client.ReadLists(ctx)
		if err != nil {
			return fail(err)
		}
		requested, missing, err = (github.Snapshot{Lists: lists}).Resolve(cfg.Lists)
		if err != nil {
			return fail(err)
		}
		if len(missing) > 0 && !cfg.CreateLists {
			return fail(fmt.Errorf("unknown star lists: %s; use --create-lists to permit creation", strings.Join(missing, ", ")))
		}
		if err := client.CheckListSupport(ctx, len(missing) > 0); err != nil {
			return fail(err)
		}
		snapshot, err = client.ReadMemberships(ctx, lists)
		if err != nil {
			return fail(err)
		}
	}
	if err := start("Discovering repositories for " + cfg.Owner); err != nil {
		return fail(err)
	}
	discovery, err := client.Discover(ctx, cfg, snapshot, requested)
	if err != nil {
		return fail(err)
	}
	if err := activity.Stop(); err != nil {
		return fail(fmt.Errorf("write progress: %w", err))
	}
	doc.Owner, doc.DiscoveryComplete, doc.LimitReached = discovery.Owner, discovery.Complete, discovery.LimitReached
	doc.Summary.Scanned, doc.Summary.Matched, doc.Summary.Unchanged = discovery.Scanned, discovery.Matched, discovery.Unchanged
	doc.Summary.Selected = len(discovery.Selected)
	if len(discovery.Selected) > 0 {
		for _, name := range missing {
			doc.ListChanges = append(doc.ListChanges, ListChange{Name: name, Visibility: cfg.ListVisibility, Status: "not_attempted"})
		}
	}
	for index := range doc.ListChanges {
		change := &doc.ListChanges[index]
		if cfg.DryRun {
			change.Status = "planned"
			continue
		}
		if err := start("Creating star list " + change.Name); err != nil {
			return fail(err)
		}
		list, err := client.CreateList(ctx, change.Name, cfg.ListVisibility == "private")
		if err != nil {
			change.Status, change.Error = "failed", err.Error()
			return fail(fmt.Errorf("create list %q: %w", change.Name, err))
		}
		change.ID, change.Status = list.ID, "created"
		requested[change.Name] = list.ID
		doc.Summary.ListsCreated++
	}
	names := make([]string, len(discovery.Selected))
	for index, repo := range discovery.Selected {
		names[index] = repo.NameWithOwner
	}
	outputWidth := 0
	if o.Terminal {
		outputWidth = o.OutputWidth
	}
	nameWidth := humanRepositoryWidth(names, outputWidth)
	for index, repo := range discovery.Selected {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if !cfg.DryRun {
			if err := start(fmt.Sprintf("Updating %s (%d/%d)", repo.NameWithOwner, index+1, len(discovery.Selected))); err != nil {
				return fail(err)
			}
		}
		result, err := stars.Apply(ctx, client, repo, snapshot, cfg.Lists, requested, cfg.DryRun)
		progressErr := activity.Stop()
		doc.Results = append(doc.Results, result)
		doc.Summary.add(result)
		if cfg.Format == "human" {
			if err := renderHumanResult(o.Out, result, o.Color, o.Terminal, nameWidth, outputWidth); err != nil {
				return fail(fmt.Errorf("write repository result: %w", err))
			}
		}
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if progressErr != nil {
			return fail(fmt.Errorf("write progress: %w", progressErr))
		}
		if github.Fatal(err) {
			return fail(err)
		}
	}
	if err := activity.Stop(); err != nil {
		return fail(fmt.Errorf("write progress: %w", err))
	}
	if doc.Summary.Failed > 0 {
		return doc, 1
	}
	return doc, 0
}
