package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/jrmash/gh-star-repos/internal/github"
	"github.com/jrmash/gh-star-repos/internal/testutil"
)

type invocation struct {
	Doc      Document
	Out, Err string
	Code     int
}

func invoke(t *testing.T, api *testutil.API, args []string, env map[string]string, contexts ...context.Context) invocation {
	t.Helper()
	cwd := t.TempDir()
	if env == nil {
		env = map[string]string{}
	}
	env["GH_CONFIG_DIR"] = filepath.Join(cwd, "config")
	var out, stderr bytes.Buffer
	ctx := context.Background()
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	code := Execute(ctx, append([]string{"--format", "json", "--sleep", "0"}, args...), Options{
		Version: "test", Out: &out, Err: &stderr, Runner: api,
		Getwd:     func() (string, error) { return cwd, nil },
		LookupEnv: func(key string) (string, bool) { value, ok := env[key]; return value, ok },
		Host:      func() string { return "github.com" },
	})
	result := invocation{Out: out.String(), Err: stderr.String(), Code: code}
	if len(out.Bytes()) > 0 && !slices.Contains(args, "--help") && !slices.Contains(args, "--version") {
		if err := json.Unmarshal(out.Bytes(), &result.Doc); err != nil {
			t.Fatalf("stdout is not one JSON document: %s: %v", out.String(), err)
		}
	}
	return result
}

func TestDefaultsIncludeArchivesAndAllVisibilityExceptForks(t *testing.T) {
	a := testutil.NewAPI(5)
	a.Repositories[1].IsFork = true
	a.Repositories[2].IsArchived = true
	a.Repositories[3].Visibility = "PRIVATE"
	a.Repositories[4].Visibility = "INTERNAL"
	r := invoke(t, a, []string{"org"}, nil)
	if r.Code != 0 || r.Doc.Summary.Starred != 4 || a.Count("AddStar") != 4 || *a.Repositories[1].ViewerHasStarred {
		t.Fatalf("wrong repository defaults: %+v %s", r.Doc, r.Err)
	}
	before := a.Mutations()
	r = invoke(t, a, []string{"org"}, nil)
	if r.Code != 0 || r.Doc.Summary.Selected != 0 || r.Doc.Summary.Unchanged != 4 || a.Mutations() != before {
		t.Fatalf("completed rerun wrote changes: %+v %s", r.Doc, r.Err)
	}
}

func TestDryRunPlansPrivateListsAndAllActionsWithoutWrites(t *testing.T) {
	a := testutil.NewAPI(3)
	starred := true
	a.Repositories[0].ViewerHasStarred = &starred
	a.Lists = []github.UserList{{ID: "other", Name: "Other"}}
	a.Memberships["R0"] = []string{"other"}
	r := invoke(t, a, []string{"org", "--list", "Go", "--create-lists", "--dry-run", "-m", "2"}, nil)
	if r.Code != 0 || a.Mutations() != 0 || len(a.Lists) != 1 || r.Doc.Summary.Planned != 2 || r.Doc.Summary.Selected != 2 {
		t.Fatalf("dry run changed state: %+v %s", r.Doc, r.Err)
	}
	if len(r.Doc.ListChanges) != 1 || r.Doc.ListChanges[0].Status != "planned" || r.Doc.ListChanges[0].Visibility != "private" {
		t.Fatalf("missing planned list creation: %+v", r.Doc.ListChanges)
	}
	if r.Doc.Results[0].Star.Status != "unchanged" || r.Doc.Results[0].Lists.Status != "planned" || r.Doc.Results[1].Star.Status != "planned" {
		t.Fatalf("incorrect action plan: %+v", r.Doc.Results)
	}
}

func TestListAssignmentPreservesEveryExistingMembership(t *testing.T) {
	a := testutil.NewAPI(2)
	starred := true
	a.Repositories[0].ViewerHasStarred = &starred
	a.Lists = []github.UserList{{ID: "A", Name: "Other A"}, {ID: "B", Name: "Other B", IsPrivate: true}, {ID: "Go", Name: "Go"}, {ID: "Tools", Name: "Tools"}}
	a.Memberships["R0"] = []string{"A", "B"}
	r := invoke(t, a, []string{"org", "--list", "Go", "--list", "Tools"}, nil)
	if r.Code != 0 || r.Doc.Summary.Starred != 1 || r.Doc.Summary.ListUpdates != 2 || !reflect.DeepEqual(a.Memberships["R0"], []string{"A", "B", "Go", "Tools"}) {
		t.Fatalf("memberships lost: %+v %s %+v", r.Doc, r.Err, a.Memberships)
	}
	before := a.Mutations()
	r = invoke(t, a, []string{"org", "--list", "Go", "--list", "Tools"}, nil)
	if r.Code != 0 || a.Mutations() != before || r.Doc.Summary.Selected != 0 {
		t.Fatalf("list-complete rerun wrote changes: %+v %s", r.Doc, r.Err)
	}
}

func TestMissingAndUnsupportedListsFailBeforeAnyWrites(t *testing.T) {
	a := testutil.NewAPI(2)
	r := invoke(t, a, []string{"org", "--list", "missing"}, nil)
	if r.Code != 1 || a.Mutations() != 0 || !strings.Contains(r.Err, "--create-lists") || a.Count("GetRepositories") != 0 {
		t.Fatalf("missing-list preflight: %+v %s", r.Doc, r.Err)
	}
	a.Lists = []github.UserList{{ID: "L", Name: "Go"}}
	a.Fail = func(_ context.Context, call testutil.Call) (string, error, bool) {
		if call.Operation == "CheckListSupport" {
			return testutil.Envelope(map[string]any{"mutationType": nil}), nil, true
		}
		return "", nil, false
	}
	r = invoke(t, a, []string{"org", "--list", "Go"}, nil)
	if r.Code != 1 || a.Mutations() != 0 || !strings.Contains(r.Err, "required star-list APIs") {
		t.Fatalf("unsupported-host preflight: %+v %s", r.Doc, r.Err)
	}
}

func TestNewListCreationPrecedesStarsAndHonorsVisibility(t *testing.T) {
	for _, visibility := range []string{"private", "public"} {
		t.Run(visibility, func(t *testing.T) {
			a := testutil.NewAPI(2)
			r := invoke(t, a, []string{"org", "--list", "Go, CLI", "--create-lists", "--list-visibility", visibility, "-m", "1"}, nil)
			if r.Code != 0 || len(a.Lists) != 1 || a.Lists[0].Name != "Go, CLI" || a.Lists[0].IsPrivate != (visibility == "private") || r.Doc.Summary.ListsCreated != 1 || r.Doc.Summary.Starred != 1 || r.Doc.Summary.ListUpdates != 1 {
				t.Fatalf("creation: %+v %s %+v", r.Doc, r.Err, a.Lists)
			}
			ops := []string{}
			for _, call := range a.Calls {
				if strings.HasPrefix(call.Query, "mutation") {
					ops = append(ops, call.Operation)
				}
			}
			if !reflect.DeepEqual(ops, []string{"CreateList", "AddStar", "SetLists"}) {
				t.Fatalf("unexpected write order: %v", ops)
			}
		})
	}
	a := testutil.NewAPI(0)
	r := invoke(t, a, []string{"org", "--list", "empty", "--create-lists"}, nil)
	if r.Code != 0 || a.Mutations() != 0 || len(r.Doc.ListChanges) != 0 {
		t.Fatal("empty selection created an empty list")
	}
}

func TestPartialSuccessIsRepairableOnRerun(t *testing.T) {
	a := testutil.NewAPI(2)
	a.Lists = []github.UserList{{ID: "Go", Name: "Go"}}
	a.Fail = func(_ context.Context, call testutil.Call) (string, error, bool) {
		if call.Operation == "SetLists" && call.Variables["id"] == "R0" {
			out, err := testutil.Failure(403, "list assignment denied", "")
			return out, err, true
		}
		return "", nil, false
	}
	r := invoke(t, a, []string{"org", "--list", "Go"}, nil)
	if r.Code != 1 || r.Doc.Summary.Starred != 2 || r.Doc.Summary.ListUpdates != 1 || r.Doc.Summary.Failed != 1 || r.Doc.Results[0].Star.Status != "starred" || r.Doc.Results[0].Lists.Status != "failed" {
		t.Fatalf("partial success was lost: %+v %s", r.Doc, r.Err)
	}
	a.Fail = nil
	starsBefore := a.Count("AddStar")
	r = invoke(t, a, []string{"org", "--list", "Go"}, nil)
	if r.Code != 0 || r.Doc.Summary.Selected != 1 || r.Doc.Summary.ListUpdates != 1 || r.Doc.Summary.Starred != 0 || a.Count("AddStar") != starsBefore {
		t.Fatalf("rerun did not repair list only: %+v %s", r.Doc, r.Err)
	}
}

func TestRepositoryFailuresContinueButDoNotRefillBoundedBatch(t *testing.T) {
	a := testutil.NewAPI(3)
	a.Fail = func(_ context.Context, call testutil.Call) (string, error, bool) {
		if call.Operation == "AddStar" && call.Variables["id"] == "R0" {
			out, err := testutil.Failure(403, "repository permission denied", "")
			return out, err, true
		}
		return "", nil, false
	}
	r := invoke(t, a, []string{"org", "-m", "2"}, nil)
	if r.Code != 1 || r.Doc.Summary.Processed != 2 || r.Doc.Summary.Failed != 1 || r.Doc.Summary.Starred != 1 || *a.Repositories[2].ViewerHasStarred {
		t.Fatalf("batch limit/failure handling: %+v %s", r.Doc, r.Err)
	}
}

func TestDiscoveryMustCompleteBeforeWrites(t *testing.T) {
	a := testutil.NewAPI(101)
	a.Fail = func(_ context.Context, call testutil.Call) (string, error, bool) {
		if call.Operation == "GetRepositories" && call.Variables["after"] != nil {
			out, err := testutil.Failure(404, "owner disappeared on later page", "")
			return out, err, true
		}
		return "", nil, false
	}
	r := invoke(t, a, []string{"org"}, nil)
	if r.Code != 1 || a.Mutations() != 0 || len(r.Doc.Results) != 0 {
		t.Fatalf("partial discovery caused writes: %+v %s", r.Doc, r.Err)
	}
}

func TestInterruptionReportsPartialResultsAndRerunResumes(t *testing.T) {
	a := testutil.NewAPI(3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Fail = func(ctx context.Context, call testutil.Call) (string, error, bool) {
		if call.Operation == "AddStar" && call.Variables["id"] == "R1" {
			cancel()
			return "", ctx.Err(), true
		}
		return "", nil, false
	}
	r := invoke(t, a, []string{"org"}, nil, ctx)
	if r.Code != 130 || r.Doc.Summary.Starred != 1 || !*a.Repositories[0].ViewerHasStarred || *a.Repositories[2].ViewerHasStarred {
		t.Fatalf("interruption: %+v %s", r.Doc, r.Err)
	}
	a.Fail = nil
	r = invoke(t, a, []string{"org"}, nil)
	if r.Code != 0 || r.Doc.Summary.Selected != 2 || r.Doc.Summary.Starred != 2 {
		t.Fatalf("resume: %+v %s", r.Doc, r.Err)
	}
}

func TestExplicitFalseAndEmptyFlagsClearEnvironment(t *testing.T) {
	a := testutil.NewAPI(1)
	env := map[string]string{"GHSR_OPT_LIST": "missing", "GHSR_OPT_EXCLUDE": "repo-*", "GHSR_OPT_TOPIC": "missing", "GHSR_OPT_LANGUAGE": "Rust", "GHSR_OPT_DRY_RUN": "true"}
	r := invoke(t, a, []string{"org", "--list=", "--exclude=", "--topic=", "--language=", "--dry-run=false"}, env)
	if r.Code != 0 || r.Doc.DryRun || r.Doc.Summary.Starred != 1 || a.Count("GetLists") != 0 {
		t.Fatalf("explicit CLI overrides ignored: %+v %s", r.Doc, r.Err)
	}
}

func TestOwnerFallbackAndConfigShowSources(t *testing.T) {
	a := testutil.NewAPI(1)
	a.Owner = a.Viewer
	a.Repositories[0].Owner.Login = a.Viewer
	a.Repositories[0].NameWithOwner = a.Viewer + "/" + a.Repositories[0].Name
	r := invoke(t, a, nil, nil)
	if r.Code != 0 || r.Doc.Owner != a.Viewer || a.Count("GetViewer") != 1 || r.Doc.Summary.Starred != 1 {
		t.Fatalf("owner fallback: %+v %s", r.Doc, r.Err)
	}
	a.Calls = nil
	r = invoke(t, a, []string{"config", "show", "--sources", "--forks", "any"}, map[string]string{"GHSR_OWNER": "example"})
	var config struct {
		Settings map[string]any    `json:"settings"`
		Sources  map[string]string `json:"sources"`
	}
	if err := json.Unmarshal([]byte(r.Out), &config); err != nil {
		t.Fatal(err)
	}
	if r.Code != 0 || len(a.Calls) != 0 || config.Settings["forks"] != "any" || config.Sources["forks"] != "--forks" || config.Sources["owner"] != "GHSR_OWNER" {
		t.Fatalf("configuration reporting used API or ignored flags: %s %s", r.Out, r.Err)
	}
}

func TestHelpAndVersionBypassInvalidConfiguration(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".gh-star-repos.yml"), []byte("invalid: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--help", "--version"} {
		a := testutil.NewAPI(0)
		var out, stderr bytes.Buffer
		code := Execute(context.Background(), []string{flag}, Options{Out: &out, Err: &stderr, Runner: a, Version: "v1.2.3", Getwd: func() (string, error) { return cwd, nil }})
		if code != 0 || len(a.Calls) != 0 || out.Len() == 0 {
			t.Fatalf("%s depends on config/auth: %d %s", flag, code, stderr.String())
		}
	}
}

func TestInvalidArgumentsDoNotAccessGitHub(t *testing.T) {
	for _, args := range [][]string{{"org", "another"}, {"org", "--sleep=-1"}, {"org", "--exclude=[bad"}, {"org", "--format=xml"}, {"--no-such-flag"}} {
		a := testutil.NewAPI(0)
		r := invoke(t, a, args, nil)
		if r.Code != 2 || len(a.Calls) != 0 {
			t.Fatalf("invalid arguments contacted GitHub: %v %d %s", args, r.Code, r.Err)
		}
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestBrokenOutputStopsFurtherMutations(t *testing.T) {
	a := testutil.NewAPI(3)
	cwd := t.TempDir()
	code := Execute(context.Background(), []string{"org", "--sleep=0"}, Options{
		Out: brokenWriter{}, Err: io.Discard, Runner: a,
		Getwd: func() (string, error) { return cwd, nil }, Host: func() string { return "github.com" },
		LookupEnv: func(key string) (string, bool) { return cwd, key == "GH_CONFIG_DIR" },
	})
	if code != 1 || a.Count("AddStar") != 1 {
		t.Fatalf("continued mutations after output failure: exit=%d writes=%d", code, a.Count("AddStar"))
	}
}

func TestYAMLOutputAndHumanPartialFailure(t *testing.T) {
	for _, format := range []string{"yaml", "human"} {
		t.Run(format, func(t *testing.T) {
			a := testutil.NewAPI(1)
			var out, stderr bytes.Buffer
			cwd := t.TempDir()
			code := Execute(context.Background(), []string{"org", "--format", format, "--dry-run"}, Options{
				Out: &out, Err: &stderr, Runner: a,
				Getwd: func() (string, error) { return cwd, nil }, Host: func() string { return "github.com" },
				LookupEnv: func(key string) (string, bool) { return cwd, key == "GH_CONFIG_DIR" },
				Sleep:     func(context.Context, time.Duration) error { return fmt.Errorf("dry run must not sleep") },
			})
			if code != 0 || a.Mutations() != 0 {
				t.Fatalf("format=%s code=%d stderr=%s", format, code, stderr.String())
			}
			if format == "yaml" {
				var doc Document
				if err := yaml.Unmarshal(out.Bytes(), &doc); err != nil || doc.Summary.Planned != 1 || strings.Contains(out.String(), "Discovering") {
					t.Fatalf("invalid YAML output: %s %v", out.String(), err)
				}
			} else if strings.Count(out.String(), "Org/repo-0000") != 1 || !strings.Contains(out.String(), "dry run") || stderr.Len() != 0 {
				t.Fatalf("human progress/results: %s %s", out.String(), stderr.String())
			}
		})
	}
}
