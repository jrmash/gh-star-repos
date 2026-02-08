package github

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type UserList struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsPrivate bool   `json:"isPrivate"`
}

type Snapshot struct {
	Lists       []UserList
	Memberships map[string][]string
}

func (s Snapshot) Resolve(names []string) (map[string]string, []string, error) {
	resolved, missing := map[string]string{}, []string{}
	for _, name := range names {
		for _, list := range s.Lists {
			if list.Name == name {
				if _, exists := resolved[name]; exists {
					return nil, nil, fmt.Errorf("list name %q is ambiguous; rename the duplicate lists", name)
				}
				resolved[name] = list.ID
			}
		}
		if _, exists := resolved[name]; !exists {
			resolved[name] = "" // A planned list is missing from every repository.
			missing = append(missing, name)
		}
	}
	return resolved, missing, nil
}

func (s Snapshot) NeedsLists(repository string, requested map[string]string) bool {
	for _, id := range requested {
		if id == "" || !slices.Contains(s.Memberships[repository], id) {
			return true
		}
	}
	return false
}

func (s Snapshot) Union(repository string, requested map[string]string) []string {
	ids := slices.Clone(s.Memberships[repository])
	for _, id := range requested {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func (c *Client) CheckListSupport(ctx context.Context, create bool) error {
	var data struct {
		Mutation *struct{ Fields []Named }      `json:"mutationType"`
		Create   *struct{ InputFields []Named } `json:"createInput"`
	}
	query := `query CheckListSupport {
  mutationType: __type(name: "Mutation") { fields { name } }
  createInput: __type(name: "CreateUserListInput") { inputFields { name } }
}`
	if err := c.Do(ctx, query, nil, &data, false, nil); err != nil {
		return fmt.Errorf("check star-list API support: %w", err)
	}
	has := func(fields []Named, name string) bool {
		return slices.ContainsFunc(fields, func(field Named) bool { return field.Name == name })
	}
	if data.Mutation == nil || !has(data.Mutation.Fields, "updateUserListsForItem") ||
		create && (!has(data.Mutation.Fields, "createUserList") || data.Create == nil || !has(data.Create.InputFields, "isPrivate")) {
		return errors.New("this GitHub host does not expose the required star-list APIs")
	}
	return nil
}

func (c *Client) ReadLists(ctx context.Context) ([]UserList, error) {
	result, seenIDs, cursors := []UserList{}, map[string]bool{}, map[string]bool{}
	var after any
	for {
		var data struct {
			Viewer *struct {
				Lists *struct {
					Nodes    []*UserList `json:"nodes"`
					PageInfo *PageInfo   `json:"pageInfo"`
				} `json:"lists"`
			} `json:"viewer"`
		}
		query := `query GetLists($after: String) { viewer { lists(first: 100, after: $after) {
  nodes { id name isPrivate } pageInfo { hasNextPage endCursor }
} } }`
		if err := c.Do(ctx, query, map[string]any{"after": after}, &data, false, nil); err != nil {
			return nil, fmt.Errorf("read star lists: %w", err)
		}
		if data.Viewer == nil || data.Viewer.Lists == nil {
			return nil, errors.New("GitHub returned no star lists connection")
		}
		lists := data.Viewer.Lists
		if err := validatePage(lists.PageInfo, cursors); err != nil {
			return nil, err
		}
		for _, list := range lists.Nodes {
			if list == nil || list.ID == "" || list.Name == "" {
				return nil, errors.New("GitHub returned an invalid star list")
			}
			if !seenIDs[list.ID] {
				seenIDs[list.ID] = true
				result = append(result, *list)
			}
		}
		if !lists.PageInfo.HasNextPage {
			return result, nil
		}
		after = lists.PageInfo.EndCursor
	}
}

func (c *Client) ReadMemberships(ctx context.Context, lists []UserList) (Snapshot, error) {
	result := Snapshot{Lists: lists, Memberships: map[string][]string{}}
	type pending struct {
		id    string
		after any
	}
	queue := make([]pending, 0, len(lists))
	cursors := map[string]map[string]bool{}
	for _, list := range lists {
		queue = append(queue, pending{id: list.ID})
		cursors[list.ID] = map[string]bool{}
	}
	for len(queue) > 0 {
		size := min(10, len(queue))
		batch := slices.Clone(queue[:size])
		queue = queue[size:]
		declarations, selections := []string{}, []string{}
		variables := map[string]any{}
		for index, item := range batch {
			declarations = append(declarations, fmt.Sprintf("$id%d: ID!, $after%d: String", index, index))
			selections = append(selections, fmt.Sprintf(`l%d: node(id: $id%d) { ... on UserList {
  id items(first: 100, after: $after%d) { nodes { ... on Repository { id } } pageInfo { hasNextPage endCursor } }
} }`, index, index, index))
			variables[fmt.Sprintf("id%d", index)] = item.id
			variables[fmt.Sprintf("after%d", index)] = item.after
		}
		query := "query GetListItems(" + strings.Join(declarations, ", ") + ") { " + strings.Join(selections, "\n") + " }"
		var data map[string]*struct {
			ID    string `json:"id"`
			Items *struct {
				Nodes    []*struct{ ID string } `json:"nodes"`
				PageInfo *PageInfo              `json:"pageInfo"`
			} `json:"items"`
		}
		if err := c.Do(ctx, query, variables, &data, false, nil); err != nil {
			return Snapshot{}, fmt.Errorf("read list memberships: %w", err)
		}
		for index, item := range batch {
			list := data[fmt.Sprintf("l%d", index)]
			if list == nil || list.ID != item.id || list.Items == nil {
				return Snapshot{}, errors.New("star list disappeared or its contents were inaccessible")
			}
			if err := validatePage(list.Items.PageInfo, cursors[item.id]); err != nil {
				return Snapshot{}, err
			}
			for _, repo := range list.Items.Nodes {
				if repo == nil || repo.ID == "" {
					return Snapshot{}, errors.New("inaccessible repository in star list; cannot preserve memberships")
				}
				if !slices.Contains(result.Memberships[repo.ID], item.id) {
					result.Memberships[repo.ID] = append(result.Memberships[repo.ID], item.id)
				}
			}
			if list.Items.PageInfo.HasNextPage {
				queue = append(queue, pending{id: item.id, after: list.Items.PageInfo.EndCursor})
			}
		}
	}
	return result, nil
}
