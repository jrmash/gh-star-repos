package github_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jrmash/gh-star-repos/internal/github"
	"github.com/jrmash/gh-star-repos/internal/testutil"
)

func TestMutationsRejectUnconfirmedOutcomes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		data any
		call func(*github.Client) error
	}{
		{"star", map[string]any{"addStar": nil}, func(c *github.Client) error { return c.AddStar(ctx, "R0") }},
		{"list item", map[string]any{"updateUserListsForItem": nil}, func(c *github.Client) error { return c.SetLists(ctx, "R0", []string{"Go"}) }},
		{"list membership", map[string]any{"updateUserListsForItem": map[string]any{"item": map[string]string{"id": "R0"}, "lists": []map[string]string{{"id": "other"}}}}, func(c *github.Client) error { return c.SetLists(ctx, "R0", []string{"Go"}) }},
		{"created list", map[string]any{"createUserList": nil}, func(c *github.Client) error { _, err := c.CreateList(ctx, "Go", true); return err }},
		{"new list privacy", map[string]any{"createUserList": map[string]any{"list": github.UserList{ID: "L", Name: "Go", IsPrivate: false}}}, func(c *github.Client) error { _, err := c.CreateList(ctx, "Go", true); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testutil.NewAPI(1)
			a.Fail = func(context.Context, testutil.Call) (string, error, bool) {
				return testutil.Envelope(tc.data), nil, true
			}
			if err := tc.call(client(a, &clock{})); err == nil || !strings.Contains(err.Error(), "confirm") && !strings.Contains(err.Error(), "unexpected list membership") {
				t.Fatalf("unconfirmed mutation accepted: %v", err)
			}
			if len(a.Calls) != 1 {
				t.Fatal("unconfirmed result was retried")
			}
		})
	}
}

func TestTruncatedResponseIsRetried(t *testing.T) {
	a, clock := testutil.NewAPI(1), &clock{}
	a.Fail = func(_ context.Context, call testutil.Call) (string, error, bool) {
		if a.Count(call.Operation) == 1 {
			return "HTTP/2.0 200 OK\n\n{\"data\":", nil, true
		}
		return "", nil, false
	}
	if err := client(a, clock).AddStar(context.Background(), "R0"); err != nil || a.Count("AddStar") != 2 || !*a.Repositories[0].ViewerHasStarred {
		t.Fatalf("truncated response recovery: %v", err)
	}
}

func TestCreateRetriesOnlyAfterCheckingForCommittedList(t *testing.T) {
	a, clock := testutil.NewAPI(0), &clock{}
	a.Fail = func(_ context.Context, call testutil.Call) (string, error, bool) {
		if call.Operation == "CreateList" && a.Count("CreateList") == 1 {
			output, err := testutil.Failure(503, "temporarily unavailable", "")
			return output, err, true
		}
		return "", nil, false
	}
	list, err := client(a, clock).CreateList(context.Background(), "Go", true)
	if err != nil || list.Name != "Go" || !list.IsPrivate || len(a.Lists) != 1 || a.Count("CreateList") != 2 || a.Count("GetLists") != 1 {
		t.Fatalf("list creation recovery: %+v %v", list, err)
	}
}

func TestMalformedResponsesFailWithoutFurtherRequests(t *testing.T) {
	for _, response := range []string{
		"HTTP/2.0 200", "HTTP/2.0\n\n{}", "HTTP/2.0 invalid\n\n{}", "HTTP/2.0 200 OK\ninvalid header\n\n{}",
		"HTTP/2.0 200 OK\n\nnot JSON", "HTTP/2.0 200 OK\n\n{}", "HTTP/2.0 200 OK\n\n{\"data\":[]}",
	} {
		t.Run(response, func(t *testing.T) {
			a := testutil.NewAPI(0)
			a.Fail = func(context.Context, testutil.Call) (string, error, bool) { return response, nil, true }
			if _, err := client(a, &clock{}).Viewer(context.Background()); err == nil || len(a.Calls) != 1 {
				t.Fatalf("malformed response accepted or retried: %v", err)
			}
		})
	}
}

func TestSuccessfulSleep(t *testing.T) {
	for _, delay := range []time.Duration{0, time.Millisecond} {
		if err := github.Sleep(context.Background(), delay); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExhaustedSuccessfulReadWaitsForResetBeforeNextRequest(t *testing.T) {
	a, clock := testutil.NewAPI(0), &clock{now: time.Unix(0, 0)}
	a.Fail = func(_ context.Context, call testutil.Call) (string, error, bool) {
		if a.Count(call.Operation) == 1 {
			return "HTTP/2.0 200 OK\r\nX-Ratelimit-Remaining: 0\r\nX-Ratelimit-Reset: 45\r\n\r\n" +
				`{"data":{"viewer":{"login":"me"}}}`, nil, true
		}
		return "", nil, false
	}
	c := client(a, clock)
	reasons := []string{}
	c.OnWait = func(_ time.Duration, reason string) { reasons = append(reasons, reason) }
	for i := 0; i < 2; i++ {
		if viewer, err := c.Viewer(context.Background()); err != nil || viewer != "me" {
			t.Fatalf("viewer read: %q %v", viewer, err)
		}
	}
	if len(clock.waits) != 1 || clock.waits[0] != 45*time.Second || len(reasons) != 1 || reasons[0] != "GitHub rate limit reset" || a.Count("GetViewer") != 2 {
		t.Fatalf("read ignored exhausted rate limit: waits=%v reasons=%v calls=%d", clock.waits, reasons, a.Count("GetViewer"))
	}
}

func TestViewerRejectsAnInvalidAuthenticatedIdentity(t *testing.T) {
	a := testutil.NewAPI(0)
	a.Viewer = "unexpected/owner"
	if _, err := client(a, &clock{}).Viewer(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid authenticated user") || a.Mutations() != 0 {
		t.Fatalf("invalid authenticated owner was accepted: %v", err)
	}
}

func TestFailedReconciliationDoesNotRepeatListCreation(t *testing.T) {
	a := testutil.NewAPI(0)
	a.Fail = func(_ context.Context, call testutil.Call) (string, error, bool) {
		if call.Operation == "CreateList" {
			output, err := testutil.Failure(503, "uncertain outcome", "")
			return output, err, true
		}
		if call.Operation == "GetLists" {
			output, err := testutil.Failure(401, "bad credentials", "")
			return output, err, true
		}
		return "", nil, false
	}
	_, err := client(a, &clock{}).CreateList(context.Background(), "Go", true)
	if err == nil || !strings.Contains(err.Error(), "reconcile list creation") || !github.Fatal(err) || a.Count("CreateList") != 1 || a.Count("GetLists") != 1 {
		t.Fatalf("reconciliation failure repeated non-idempotent creation: %v, calls=%v", err, a.Calls)
	}
}
