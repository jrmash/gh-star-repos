// Package testutil provides a stateful, offline GitHub API for behavioral tests.
package testutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jrmash/gh-star-repos/internal/github"
)

type Call struct {
	Operation string
	Query     string
	Variables map[string]any
}

type API struct {
	Owner        string
	Viewer       string
	Repositories []github.Repository
	Lists        []github.UserList
	Memberships  map[string][]string
	Calls        []Call
	Fail         func(context.Context, Call) (string, error, bool)
}

func NewAPI(count int) *API {
	a := &API{Owner: "Org", Viewer: "me", Memberships: map[string][]string{}}
	for index := 0; index < count; index++ {
		starred := false
		r := github.Repository{ID: fmt.Sprintf("R%d", index), Name: fmt.Sprintf("repo-%04d", index),
			CreatedAt:  time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC).Add(-time.Duration(index) * time.Minute),
			Visibility: "PUBLIC", ViewerHasStarred: &starred}
		r.Owner.Login = a.Owner
		r.NameWithOwner = a.Owner + "/" + r.Name
		r.URL = "https://github.com/" + r.NameWithOwner
		a.Repositories = append(a.Repositories, r)
	}
	return a
}

func Envelope(data any) string {
	body, _ := json.Marshal(map[string]any{"data": data})
	return "HTTP/2.0 200 OK\r\nContent-Type: application/json\r\nX-Ratelimit-Remaining: 1000\r\n\r\n" + string(body)
}

func Failure(status int, message string, headers string) (string, error) {
	body, _ := json.Marshal(map[string]string{"message": message})
	return fmt.Sprintf("HTTP/2.0 %d Error\r\nContent-Type: application/json\r\n%s\r\n%s", status, headers, body), errors.New("gh api failed")
}

func (a *API) Run(ctx context.Context, name string, args ...string) (string, error) {
	if name != "gh" || len(args) < 2 || args[0] != "api" || args[1] != "graphql" {
		return "", fmt.Errorf("unexpected external command %s %v", name, args)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	call := Call{Variables: map[string]any{}}
	for index := 0; index < len(args)-1; index++ {
		if args[index] != "--raw-field" && args[index] != "--field" {
			continue
		}
		kind, pair := args[index], args[index+1]
		index++
		key, value, _ := strings.Cut(pair, "=")
		if key == "query" {
			call.Query = value
			continue
		}
		if strings.HasSuffix(key, "[]") {
			key = strings.TrimSuffix(key, "[]")
			items, _ := call.Variables[key].([]string)
			call.Variables[key] = append(items, value)
		} else if kind == "--field" {
			var parsed any
			if err := json.Unmarshal([]byte(value), &parsed); err != nil {
				return "", err
			}
			call.Variables[key] = parsed
		} else {
			call.Variables[key] = value
		}
	}
	fields := strings.Fields(call.Query)
	if len(fields) < 2 {
		return "", errors.New("missing GraphQL operation name")
	}
	call.Operation, _, _ = strings.Cut(fields[1], "(")
	a.Calls = append(a.Calls, call)
	if a.Fail != nil {
		if output, err, handled := a.Fail(ctx, call); handled {
			return output, err
		}
	}
	switch call.Operation {
	case "GetViewer":
		return Envelope(map[string]any{"viewer": map[string]string{"login": a.Viewer}}), nil
	case "CheckListSupport":
		return Envelope(map[string]any{
			"mutationType": map[string]any{"fields": []map[string]string{{"name": "updateUserListsForItem"}, {"name": "createUserList"}}},
			"createInput":  map[string]any{"inputFields": []map[string]string{{"name": "isPrivate"}}},
		}), nil
	case "GetLists":
		start, end := bounds(call.Variables["after"], len(a.Lists))
		return Envelope(map[string]any{"viewer": map[string]any{"lists": map[string]any{
			"nodes": a.Lists[start:end], "pageInfo": pageInfo(end, len(a.Lists)),
		}}}), nil
	case "GetListItems":
		data := map[string]any{}
		for index := 0; ; index++ {
			id, exists := call.Variables[fmt.Sprintf("id%d", index)]
			if !exists {
				break
			}
			ids := []string{}
			for repo, memberships := range a.Memberships {
				if slices.Contains(memberships, id.(string)) {
					ids = append(ids, repo)
				}
			}
			sort.Strings(ids)
			start, end := bounds(call.Variables[fmt.Sprintf("after%d", index)], len(ids))
			nodes := []map[string]string{}
			for _, repo := range ids[start:end] {
				nodes = append(nodes, map[string]string{"id": repo})
			}
			data[fmt.Sprintf("l%d", index)] = map[string]any{"id": id, "items": map[string]any{"nodes": nodes, "pageInfo": pageInfo(end, len(ids))}}
		}
		return Envelope(data), nil
	case "GetRepositories":
		repos := []github.Repository{}
		for _, repo := range a.Repositories {
			if archived, ok := call.Variables["archived"].(bool); ok && repo.IsArchived != archived {
				continue
			}
			if fork, ok := call.Variables["fork"].(bool); ok && repo.IsFork != fork {
				continue
			}
			if visibility, ok := call.Variables["visibility"].(string); ok && !strings.EqualFold(repo.Visibility, visibility) {
				continue
			}
			repos = append(repos, repo)
		}
		start, end := bounds(call.Variables["after"], len(repos))
		return Envelope(map[string]any{"repositoryOwner": map[string]any{"login": a.Owner, "repositories": map[string]any{
			"nodes": repos[start:end], "pageInfo": pageInfo(end, len(repos)),
		}}}), nil
	case "AddStar":
		id := call.Variables["id"].(string)
		for index := range a.Repositories {
			if a.Repositories[index].ID == id {
				starred := true
				a.Repositories[index].ViewerHasStarred = &starred
				return Envelope(map[string]any{"addStar": map[string]any{"starrable": map[string]any{"id": id, "viewerHasStarred": true}}}), nil
			}
		}
		return Failure(404, "repository disappeared", "")
	case "CreateList":
		list := github.UserList{ID: fmt.Sprintf("L%d", len(a.Lists)+1), Name: call.Variables["name"].(string), IsPrivate: call.Variables["private"].(bool)}
		a.Lists = append(a.Lists, list)
		return Envelope(map[string]any{"createUserList": map[string]any{"list": list}}), nil
	case "SetLists":
		id := call.Variables["id"].(string)
		ids, ok := call.Variables["listIds"].([]string)
		if !ok || len(ids) == 0 {
			return "", errors.New("listIds must be a nonempty GraphQL array")
		}
		a.Memberships[id] = slices.Clone(ids)
		lists := []map[string]string{}
		for _, list := range ids {
			lists = append(lists, map[string]string{"id": list})
		}
		return Envelope(map[string]any{"updateUserListsForItem": map[string]any{"item": map[string]string{"id": id}, "lists": lists}}), nil
	default:
		return "", fmt.Errorf("unexpected GraphQL operation %q", call.Operation)
	}
}

func (a *API) Count(operation string) int {
	count := 0
	for _, call := range a.Calls {
		if call.Operation == operation {
			count++
		}
	}
	return count
}

func (a *API) Mutations() int {
	return a.Count("AddStar") + a.Count("SetLists") + a.Count("CreateList")
}

func bounds(cursor any, total int) (int, int) {
	start := 0
	if text, ok := cursor.(string); ok {
		start, _ = strconv.Atoi(text)
	}
	start = min(start, total)
	return start, min(start+100, total)
}

func pageInfo(end, total int) map[string]any {
	return map[string]any{"hasNextPage": end < total, "endCursor": strconv.Itoa(end)}
}
