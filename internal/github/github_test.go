package github_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jrmash/gh-star-repos/internal/github"
	"github.com/jrmash/gh-star-repos/internal/settings"
	"github.com/jrmash/gh-star-repos/internal/testutil"
)

type runnerFunc func(context.Context, string, ...string) (string, error)

func (f runnerFunc) Run(ctx context.Context, name string, args ...string) (string, error) {
	return f(ctx, name, args...)
}

type clock struct {
	now   time.Time
	waits []time.Duration
}

func (c *clock) sleep(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.waits = append(c.waits, delay)
	c.now = c.now.Add(delay)
	return nil
}

func client(a *testutil.API, c *clock) *github.Client {
	return &github.Client{Runner: a, Host: "github.com", Now: func() time.Time { return c.now }, Sleep: c.sleep, Pause: time.Second}
}

func config() settings.Config {
	return settings.Config{Owner: "org", Archived: "any", Forks: "exclude", Visibility: "any"}
}

func TestDiscoveryBeyondSearchLimit(t *testing.T) {
	a := testutil.NewAPI(1107)
	result, err := client(a, &clock{}).Discover(context.Background(), config(), github.Snapshot{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selected) != 1107 || !result.Complete || result.Scanned != 1107 || a.Count("GetRepositories") != 12 || a.Mutations() != 0 {
		t.Fatalf("pagination: %+v, calls=%d", result, len(a.Calls))
	}
	if result.Selected[0].ID != "R0" || result.Selected[1106].ID != "R1106" {
		t.Fatal("creation order changed")
	}
	q := a.Calls[0].Query
	if !strings.Contains(q, "ownerAffiliations: [OWNER]") || !strings.Contains(q, "field: CREATED_AT, direction: DESC") || !strings.Contains(q, "viewerHasStarred") {
		t.Fatal("query must request owned repositories, ordering, and star status together")
	}
}

func TestLimitSkipsCompletedAndCountsListRepairs(t *testing.T) {
	a := testutil.NewAPI(230)
	starred := true
	for index := 0; index < 205; index++ {
		a.Repositories[index].ViewerHasStarred = &starred
	}
	cfg := config()
	cfg.MaxRepos = 2
	result, err := client(a, &clock{}).Discover(context.Background(), cfg, github.Snapshot{}, nil)
	if err != nil || len(result.Selected) != 2 || result.Selected[0].ID != "R205" || a.Count("GetRepositories") != 3 || !result.Complete || !result.LimitReached {
		t.Fatalf("limit counted completed repositories: %+v %v", result, err)
	}
	a.Calls = nil
	snapshot := github.Snapshot{Memberships: map[string][]string{"R0": {"L1"}}}
	result, err = client(a, &clock{}).Discover(context.Background(), cfg, snapshot, map[string]string{"Go": "L1"})
	if err != nil || len(result.Selected) != 2 || result.Selected[0].ID != "R1" || result.Selected[1].ID != "R2" || result.Complete || a.Count("GetRepositories") != 1 {
		t.Fatalf("list repair did not consume a slot: %+v %v", result, err)
	}
}

func TestDiscoveryRejectsBrokenPagesAndIdentity(t *testing.T) {
	for _, issue := range []string{"owner", "repository owner", "star status", "date order", "cursor", "GraphQL error", "null page", "null repo"} {
		t.Run(issue, func(t *testing.T) {
			a := testutil.NewAPI(101)
			switch issue {
			case "owner":
				a.Owner = "different"
			case "repository owner":
				a.Repositories[0].Owner.Login = "different"
			case "star status":
				a.Repositories[0].ViewerHasStarred = nil
			case "date order":
				a.Repositories[100].CreatedAt = a.Repositories[0].CreatedAt.Add(time.Hour)
			case "cursor":
				a.Fail = func(_ context.Context, call testutil.Call) (string, error, bool) {
					return testutil.Envelope(map[string]any{"repositoryOwner": map[string]any{"login": "Org", "repositories": map[string]any{
						"nodes": a.Repositories[:1], "pageInfo": map[string]any{"hasNextPage": true, "endCursor": "same"},
					}}}), nil, true
				}
			case "GraphQL error":
				a.Fail = func(_ context.Context, _ testutil.Call) (string, error, bool) {
					return `HTTP/2.0 200 OK` + "\n\n" + `{"data":{"repositoryOwner":null},"errors":[{"type":"NOT_FOUND","message":"missing owner"}]}`, nil, true
				}
			case "null page":
				a.Fail = func(_ context.Context, _ testutil.Call) (string, error, bool) {
					return testutil.Envelope(map[string]any{"repositoryOwner": map[string]any{"login": "Org", "repositories": map[string]any{"nodes": a.Repositories[:1]}}}), nil, true
				}
			case "null repo":
				a.Fail = func(_ context.Context, _ testutil.Call) (string, error, bool) {
					return testutil.Envelope(map[string]any{"repositoryOwner": map[string]any{"login": "Org", "repositories": map[string]any{"nodes": []any{nil}, "pageInfo": map[string]any{"hasNextPage": false}}}}), nil, true
				}
			}
			result, err := client(a, &clock{}).Discover(context.Background(), config(), github.Snapshot{}, nil)
			if err == nil || len(result.Selected) != 0 || a.Mutations() != 0 {
				t.Fatalf("partial discovery escaped: %+v %v", result, err)
			}
		})
	}
}

func TestFiltersCombineAndMatchCaseInsensitively(t *testing.T) {
	r := testutil.NewAPI(1).Repositories[0]
	r.PrimaryLanguage = &github.Named{Name: "Go"}
	r.RepositoryTopics.Nodes = []struct {
		Topic github.Named `json:"topic"`
	}{{Topic: github.Named{Name: "CLI"}}, {Topic: github.Named{Name: "Tools"}}}
	cfg := config()
	cfg.Language, cfg.Topics = "go", []string{"cli", "tools"}
	if !github.Matches(r, cfg) {
		t.Fatal("matching language/topics rejected")
	}
	cfg.Topics = append(cfg.Topics, "missing")
	if github.Matches(r, cfg) {
		t.Fatal("topics must use AND semantics")
	}
	cfg.Topics, cfg.Exclude = nil, []string{"REPO-*"}
	if github.Matches(r, cfg) {
		t.Fatal("case-insensitive exclusion ignored")
	}
	cfg.Exclude, cfg.Language = nil, "Rust"
	if github.Matches(r, cfg) {
		t.Fatal("primary language filter ignored")
	}
	cfg.Language, cfg.Archived = "", "only"
	if github.Matches(r, cfg) {
		t.Fatal("archived-only filter ignored")
	}
	r.IsArchived = true
	if !github.Matches(r, cfg) {
		t.Fatal("archived repository excluded")
	}
}

func TestListPaginationAndBatching(t *testing.T) {
	a := testutil.NewAPI(0)
	for index := 0; index < 23; index++ {
		id := fmt.Sprintf("L%d", index)
		a.Lists = append(a.Lists, github.UserList{ID: id, Name: id})
	}
	for index := 0; index < 205; index++ {
		a.Memberships[fmt.Sprintf("R%d", index)] = []string{"L0", "L22"}
	}
	c := client(a, &clock{})
	lists, err := c.ReadLists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.ReadMemberships(context.Background(), lists)
	if err != nil || len(snapshot.Memberships) != 205 || len(snapshot.Memberships["R204"]) != 2 {
		t.Fatalf("incomplete list snapshot: %d %v", len(snapshot.Memberships), err)
	}
	for _, call := range a.Calls {
		if call.Operation == "GetListItems" && len(call.Variables) > 20 {
			t.Fatal("more than ten independent item pages in one request")
		}
	}
	if a.Count("GetListItems") >= len(lists) || a.Mutations() != 0 {
		t.Fatalf("reads were not batched: %d", a.Count("GetListItems"))
	}
	a.Lists = append(a.Lists, github.UserList{ID: "duplicate", Name: "L0"})
	if _, _, err := (github.Snapshot{Lists: a.Lists}).Resolve([]string{"L0"}); err == nil {
		t.Fatal("ambiguous list accepted")
	}
}

func TestListMetadataPagination(t *testing.T) {
	a := testutil.NewAPI(0)
	for i := 0; i < 101; i++ {
		a.Lists = append(a.Lists, github.UserList{ID: fmt.Sprintf("L%d", i), Name: fmt.Sprintf("name%d", i)})
	}
	lists, err := client(a, &clock{}).ReadLists(context.Background())
	if err != nil || len(lists) != 101 || a.Count("GetLists") != 2 {
		t.Fatalf("list pagination: %d %v", len(lists), err)
	}
}

func TestRetriesAndRateHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, headers string
		status        int
		want          time.Duration
	}{
		{"transient", "", 503, time.Second},
		{"retry-after", "Retry-After: 7\r\n", 429, 7 * time.Second},
		{"primary-reset", "X-Ratelimit-Remaining: 0\r\nX-Ratelimit-Reset: 45\r\n", 403, 45 * time.Second},
		{"secondary", "", 429, time.Minute},
		{"retry-date", "Retry-After: Thu, 01 Jan 1970 00:00:09 GMT\r\n", 429, 9 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, clock := testutil.NewAPI(1), &clock{now: time.Unix(0, 0)}
			a.Fail = func(_ context.Context, call testutil.Call) (string, error, bool) {
				if a.Count(call.Operation) == 1 {
					out, err := testutil.Failure(tc.status, "temporary server failure", tc.headers)
					return out, err, true
				}
				return "", nil, false
			}
			if _, err := client(a, clock).Viewer(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(clock.waits, []time.Duration{tc.want}) || len(a.Calls) != 2 {
				t.Fatalf("waits=%v calls=%d", clock.waits, len(a.Calls))
			}
		})
	}
}

func TestGraphQLRateLimitsAndRetryExhaustion(t *testing.T) {
	a, clock := testutil.NewAPI(1), &clock{}
	a.Fail = func(_ context.Context, _ testutil.Call) (string, error, bool) {
		return "HTTP/2.0 200 OK\r\nRetry-After: 2\r\n\r\n" + `{"errors":[{"type":"RATE_LIMITED","message":"rate limited"}]}`, nil, true
	}
	_, err := client(a, clock).Viewer(context.Background())
	if err == nil || !github.Fatal(err) || len(a.Calls) != 4 || !reflect.DeepEqual(clock.waits, []time.Duration{2 * time.Second, 2 * time.Second, 2 * time.Second}) {
		t.Fatalf("rate exhaustion: %v calls=%d waits=%v", err, len(a.Calls), clock.waits)
	}
}

func TestMutationPacingIncludesListCreationAndRetries(t *testing.T) {
	a, clock := testutil.NewAPI(1), &clock{}
	c := client(a, clock)
	list, err := c.CreateList(context.Background(), "Go", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddStar(context.Background(), "R0"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetLists(context.Background(), "R0", []string{list.ID}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(clock.waits, []time.Duration{time.Second, time.Second}) {
		t.Fatalf("pacing must occur only between mutations: %v", clock.waits)
	}
}

func TestUncertainCreateIsReconciledBeforeRetry(t *testing.T) {
	a, clock := testutil.NewAPI(0), &clock{}
	a.Fail = func(_ context.Context, call testutil.Call) (string, error, bool) {
		if call.Operation == "CreateList" {
			a.Lists = append(a.Lists, github.UserList{ID: "created", Name: "Go", IsPrivate: true})
			out, err := testutil.Failure(502, "lost response after commit", "")
			return out, err, true
		}
		return "", nil, false
	}
	list, err := client(a, clock).CreateList(context.Background(), "Go", true)
	if err != nil || list.ID != "created" || a.Count("CreateList") != 1 || a.Count("GetLists") != 1 || len(a.Lists) != 1 {
		t.Fatalf("non-idempotent create retried: %+v %v %+v", list, err, a.Calls)
	}
}

func TestAuthorizationAndCancellationStopImmediately(t *testing.T) {
	a := testutil.NewAPI(0)
	a.Fail = func(_ context.Context, _ testutil.Call) (string, error, bool) {
		out, err := testutil.Failure(401, "Bad credentials", "")
		return out, err, true
	}
	_, err := client(a, &clock{}).Viewer(context.Background())
	if !github.Fatal(err) || len(a.Calls) != 1 {
		t.Fatalf("auth failure retried: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &github.Client{Host: "github.com", Runner: runnerFunc(func(ctx context.Context, _ string, _ ...string) (string, error) {
		cancel()
		return "", ctx.Err()
	})}
	_, err = c.Viewer(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if err := github.Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep ignored cancellation: %v", err)
	}
}
