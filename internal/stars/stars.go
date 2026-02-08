// Package stars applies a single repository's planned star and list changes.
package stars

import (
	"context"
	"slices"

	"github.com/jrmash/gh-star-repos/internal/github"
)

type Action struct {
	Status string   `json:"status" yaml:"status"`
	Added  []string `json:"added,omitempty" yaml:"added,omitempty"`
	Error  string   `json:"error,omitempty" yaml:"error,omitempty"`
}

type Result struct {
	Repository string `json:"repository" yaml:"repository"`
	URL        string `json:"url" yaml:"url"`
	Status     string `json:"status" yaml:"status"`
	Star       Action `json:"star" yaml:"star"`
	Lists      Action `json:"lists" yaml:"lists"`
}

// Apply reports star and list outcomes independently. A confirmed star remains
// successful when subsequent list assignment fails; reruns can repair that list.
func Apply(ctx context.Context, client *github.Client, repo github.Repository, snapshot github.Snapshot, names []string, requested map[string]string, dryRun bool) (Result, error) {
	result := Result{Repository: repo.NameWithOwner, URL: repo.URL, Status: "unchanged",
		Star: Action{Status: "unchanged"}, Lists: Action{Status: "unchanged"}}
	needsStar := !*repo.ViewerHasStarred
	needsLists := snapshot.NeedsLists(repo.ID, requested)
	if needsLists {
		for _, name := range names {
			id := requested[name]
			if id == "" || !slices.Contains(snapshot.Memberships[repo.ID], id) {
				result.Lists.Added = append(result.Lists.Added, name)
			}
		}
	}
	if dryRun {
		if needsStar {
			result.Star.Status = "planned"
		}
		if needsLists {
			result.Lists.Status = "planned"
		}
		result.Status = "planned"
		return result, nil
	}
	if needsStar {
		if err := client.AddStar(ctx, repo.ID); err != nil {
			result.Status = "failed"
			result.Star = Action{Status: "failed", Error: err.Error()}
			if needsLists {
				result.Lists.Status = "not_attempted"
			}
			return result, err
		}
		result.Status, result.Star.Status = "starred", "starred"
	}
	if needsLists {
		if err := client.SetLists(ctx, repo.ID, snapshot.Union(repo.ID, requested)); err != nil {
			result.Status = "failed"
			result.Lists.Status, result.Lists.Error = "failed", err.Error()
			return result, err
		}
		result.Lists.Status = "updated"
		if !needsStar {
			result.Status = "listed"
		}
	}
	return result, nil
}
