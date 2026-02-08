package github

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

func (c *Client) AddStar(ctx context.Context, id string) error {
	var data struct {
		Add *struct {
			Starrable *struct {
				ID               string `json:"id"`
				ViewerHasStarred bool   `json:"viewerHasStarred"`
			} `json:"starrable"`
		} `json:"addStar"`
	}
	query := `mutation AddStar($id: ID!) { addStar(input: {starrableId: $id}) { starrable { id viewerHasStarred } } }`
	if err := c.Do(ctx, query, map[string]any{"id": id}, &data, true, nil); err != nil {
		return err
	}
	if data.Add == nil || data.Add.Starrable == nil || data.Add.Starrable.ID != id || !data.Add.Starrable.ViewerHasStarred {
		return errors.New("GitHub did not confirm the requested star")
	}
	return nil
}

func (c *Client) SetLists(ctx context.Context, repository string, ids []string) error {
	var data struct {
		Update *struct {
			Item  *struct{ ID string } `json:"item"`
			Lists []UserList           `json:"lists"`
		} `json:"updateUserListsForItem"`
	}
	query := `mutation SetLists($id: ID!, $listIds: [ID!]!) {
  updateUserListsForItem(input: {itemId: $id, listIds: $listIds}) { item { ... on Repository { id } } lists { id } }
}`
	if err := c.Do(ctx, query, map[string]any{"id": repository, "listIds": ids}, &data, true, nil); err != nil {
		return err
	}
	if data.Update == nil || data.Update.Item == nil || data.Update.Item.ID != repository {
		return errors.New("GitHub did not confirm list assignment")
	}
	actual := make([]string, 0, len(data.Update.Lists))
	for _, list := range data.Update.Lists {
		actual = append(actual, list.ID)
	}
	expected := slices.Clone(ids)
	slices.Sort(actual)
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		return errors.New("GitHub confirmed unexpected list membership")
	}
	return nil
}

func (c *Client) CreateList(ctx context.Context, name string, private bool) (UserList, error) {
	var data struct {
		Create *struct {
			List *UserList `json:"list"`
		} `json:"createUserList"`
	}
	query := `mutation CreateList($name: String!, $private: Boolean!) {
  createUserList(input: {name: $name, isPrivate: $private}) { list { id name isPrivate } }
}`
	recover := func(ctx context.Context) (bool, error) {
		lists, err := c.ReadLists(ctx)
		if err != nil {
			return false, fmt.Errorf("reconcile list creation: %w", err)
		}
		resolved, missing, err := (Snapshot{Lists: lists}).Resolve([]string{name})
		if err != nil || len(missing) != 0 {
			return false, err
		}
		for _, list := range lists {
			if list.ID == resolved[name] {
				data.Create = &struct {
					List *UserList `json:"list"`
				}{List: &list}
				return true, nil
			}
		}
		return false, nil
	}
	if err := c.Do(ctx, query, map[string]any{"name": name, "private": private}, &data, true, recover); err != nil {
		return UserList{}, err
	}
	if data.Create == nil || data.Create.List == nil || data.Create.List.ID == "" || data.Create.List.Name != name {
		return UserList{}, errors.New("GitHub did not confirm list creation")
	}
	if data.Create.List.IsPrivate != private {
		return UserList{}, errors.New("GitHub did not confirm the requested new-list visibility")
	}
	return *data.Create.List, nil
}
