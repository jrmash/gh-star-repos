package github

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/jrmash/gh-star-repos/internal/settings"
)

const repositoryQuery = `query GetRepositories($owner: String!, $after: String, $archived: Boolean, $fork: Boolean, $visibility: RepositoryVisibility, $withLanguage: Boolean!, $withTopics: Boolean!) {
  repositoryOwner(login: $owner) {
    login
    ... on User {
      repositories(first: 100, after: $after, ownerAffiliations: [OWNER], isArchived: $archived, isFork: $fork, visibility: $visibility, orderBy: {field: CREATED_AT, direction: DESC}) { ...RepositoryPage }
    }
    ... on Organization {
      repositories(first: 100, after: $after, ownerAffiliations: [OWNER], isArchived: $archived, isFork: $fork, visibility: $visibility, orderBy: {field: CREATED_AT, direction: DESC}) { ...RepositoryPage }
    }
  }
}
fragment RepositoryPage on RepositoryConnection {
  nodes {
    id name nameWithOwner owner { login } url createdAt
    isArchived isFork visibility viewerHasStarred
    primaryLanguage @include(if: $withLanguage) { name }
    repositoryTopics(first: 100) @include(if: $withTopics) { nodes { topic { name } } pageInfo { hasNextPage endCursor } }
  }
  pageInfo { hasNextPage endCursor }
}`

var safeName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type PageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type Named struct {
	Name string `json:"name"`
}

type Repository struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	NameWithOwner    string                 `json:"nameWithOwner"`
	Owner            struct{ Login string } `json:"owner"`
	URL              string                 `json:"url"`
	CreatedAt        time.Time              `json:"createdAt"`
	IsArchived       bool                   `json:"isArchived"`
	IsFork           bool                   `json:"isFork"`
	Visibility       string                 `json:"visibility"`
	ViewerHasStarred *bool                  `json:"viewerHasStarred"`
	PrimaryLanguage  *Named                 `json:"primaryLanguage"`
	RepositoryTopics struct {
		Nodes []struct {
			Topic Named `json:"topic"`
		} `json:"nodes"`
		PageInfo PageInfo `json:"pageInfo"`
	} `json:"repositoryTopics"`
}

type Discovery struct {
	Owner        string
	Selected     []Repository
	Scanned      int
	Matched      int
	Unchanged    int
	Complete     bool
	LimitReached bool
}

func ValidOwner(name string) bool {
	return validName(name) && !strings.HasPrefix(name, "-")
}

func validName(name string) bool {
	return name != "." && name != ".." && safeName.MatchString(name)
}

func (c *Client) Viewer(ctx context.Context) (string, error) {
	var data struct {
		Viewer struct{ Login string } `json:"viewer"`
	}
	if err := c.Do(ctx, `query GetViewer { viewer { login } }`, nil, &data, false, nil); err != nil {
		return "", fmt.Errorf("resolve authenticated user: %w", err)
	}
	if !ValidOwner(data.Viewer.Login) {
		return "", errors.New("GitHub returned an invalid authenticated user")
	}
	return data.Viewer.Login, nil
}

// Discover validates complete pages before selecting work. It never returns a
// usable partial batch after an error. A bounded batch may intentionally stop
// before GitHub's final page; all counts refer to the fetched pages only.
func (c *Client) Discover(ctx context.Context, cfg settings.Config, snapshot Snapshot, requested map[string]string) (Discovery, error) {
	result := Discovery{Owner: cfg.Owner, Selected: []Repository{}}
	variables := map[string]any{
		"owner": cfg.Owner, "after": nil, "archived": stateArgument(cfg.Archived),
		"fork": stateArgument(cfg.Forks), "visibility": nil,
		"withLanguage": cfg.Language != "", "withTopics": len(cfg.Topics) > 0,
	}
	if cfg.Visibility != "any" {
		variables["visibility"] = strings.ToUpper(cfg.Visibility)
	}
	seen, cursors := map[string]bool{}, map[string]bool{}
	var previous time.Time
	for {
		var data struct {
			Owner *struct {
				Login string `json:"login"`
				Repos *struct {
					Nodes    []*Repository `json:"nodes"`
					PageInfo *PageInfo     `json:"pageInfo"`
				} `json:"repositories"`
			} `json:"repositoryOwner"`
		}
		if err := c.Do(ctx, repositoryQuery, variables, &data, false, nil); err != nil {
			return Discovery{}, fmt.Errorf("discover repositories: %w", err)
		}
		owner := data.Owner
		if owner == nil || !ValidOwner(owner.Login) || !strings.EqualFold(owner.Login, cfg.Owner) || owner.Repos == nil {
			return Discovery{}, fmt.Errorf("GitHub owner %q was not found or did not match", cfg.Owner)
		}
		result.Owner = owner.Login
		if err := validatePage(owner.Repos.PageInfo, cursors); err != nil {
			return Discovery{}, err
		}
		for _, repo := range owner.Repos.Nodes {
			if repo == nil || repo.ID == "" || !validName(repo.Name) || !strings.EqualFold(repo.Owner.Login, owner.Login) ||
				!strings.EqualFold(repo.NameWithOwner, owner.Login+"/"+repo.Name) || repo.ViewerHasStarred == nil || repo.CreatedAt.IsZero() {
				return Discovery{}, errors.New("invalid repository identity or missing star status in listing")
			}
			if repo.RepositoryTopics.PageInfo.HasNextPage {
				return Discovery{}, errors.New("repository topics response was truncated")
			}
		}
		for _, repo := range owner.Repos.Nodes {
			if seen[repo.ID] {
				continue
			}
			seen[repo.ID] = true
			if !previous.IsZero() && repo.CreatedAt.After(previous) {
				return Discovery{}, errors.New("repository listing was not ordered newest first")
			}
			previous = repo.CreatedAt
			result.Scanned++
			if !Matches(*repo, cfg) {
				continue
			}
			result.Matched++
			if *repo.ViewerHasStarred && !snapshot.NeedsLists(repo.ID, requested) {
				result.Unchanged++
				continue
			}
			if cfg.MaxRepos == 0 || len(result.Selected) < cfg.MaxRepos {
				result.Selected = append(result.Selected, *repo)
			}
		}
		info := owner.Repos.PageInfo
		result.Complete = !info.HasNextPage
		result.LimitReached = cfg.MaxRepos > 0 && len(result.Selected) == cfg.MaxRepos
		if result.Complete || result.LimitReached {
			return result, nil
		}
		variables["after"] = info.EndCursor
	}
}

func validatePage(info *PageInfo, seen map[string]bool) error {
	if info == nil || info.HasNextPage && (info.EndCursor == "" || seen[info.EndCursor]) {
		return errors.New("incomplete or inconsistent GitHub pagination")
	}
	if info.EndCursor != "" {
		seen[info.EndCursor] = true
	}
	return nil
}

func stateArgument(state string) any {
	switch state {
	case "exclude":
		return false
	case "only":
		return true
	default:
		return nil
	}
}

func Matches(repo Repository, cfg settings.Config) bool {
	stateMatches := func(state string, value bool) bool {
		return state == "any" || state == "only" && value || state == "exclude" && !value
	}
	if !stateMatches(cfg.Archived, repo.IsArchived) || !stateMatches(cfg.Forks, repo.IsFork) {
		return false
	}
	if cfg.Visibility != "any" && !strings.EqualFold(cfg.Visibility, repo.Visibility) {
		return false
	}
	if cfg.Language != "" && (repo.PrimaryLanguage == nil || !strings.EqualFold(cfg.Language, repo.PrimaryLanguage.Name)) {
		return false
	}
	for _, pattern := range cfg.Exclude {
		if match, _ := path.Match(strings.ToLower(pattern), strings.ToLower(repo.Name)); match {
			return false
		}
	}
	for _, required := range cfg.Topics {
		found := false
		for _, item := range repo.RepositoryTopics.Nodes {
			found = found || strings.EqualFold(required, item.Topic.Name)
		}
		if !found {
			return false
		}
	}
	return true
}
