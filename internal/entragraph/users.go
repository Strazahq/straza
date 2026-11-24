package entragraph

import (
	"context"
	"encoding/json"
)

// userFields are the properties Straza reads of a user: the three names it
// stores and the enabled state. accountEnabled needs User.Read.All.
const userFields = "id,userPrincipalName,displayName,mail,accountEnabled"

// UserChange is what the users feed says about one account. Removed says
// the account was deleted. Enabled is accountEnabled when Graph returned a
// value for it. ParentID is the agent identity behind an agent user, and
// empty for a person. A name Graph left out is empty.
type UserChange struct {
	ID          string
	Removed     bool
	Enabled     *bool
	UPN         string
	DisplayName string
	Mail        string
	ParentID    string
}

// UsersResult is one complete read of the users feed: the changed accounts
// in the order Graph named them, and the delta link the next read
// continues from. One account may appear more than once.
type UsersResult struct {
	Users []UserChange
	Link  string
}

// UsersDelta reads the changes of the tenant's users. With an empty link it
// starts a feed from now, which lists nobody and answers the first link.
// Otherwise it continues from link, a delta link of an earlier result. It
// walks every page, and any failure on any page returns an error and no
// partial result.
func (c *Client) UsersDelta(ctx context.Context, link string) (UsersResult, error) {
	what := "the read of the changed users"
	if link == "" {
		link = c.base + "/v1.0/users/delta?$select=" + userFields + ",identityParentId&$deltatoken=latest"
	} else if err := c.checkLink(what, link); err != nil {
		return UsersResult{}, errorf("Straza did not follow the delta link of the users it was handed, because it is not on %s. Start a fresh read with an empty link.", c.graph.Host)
	}
	var users []UserChange
	next, err := c.walk(ctx, what, link, true, func(value json.RawMessage) error {
		var list []struct {
			ID               string          `json:"id"`
			Removed          json.RawMessage `json:"@removed"`
			AccountEnabled   *bool           `json:"accountEnabled"`
			UPN              string          `json:"userPrincipalName"`
			DisplayName      string          `json:"displayName"`
			Mail             string          `json:"mail"`
			IdentityParentID string          `json:"identityParentId"`
		}
		if err := json.Unmarshal(value, &list); err != nil {
			return errorf("Microsoft Graph answered %s with a list Straza cannot read, so Straza counts the read as incomplete.", what)
		}
		for _, u := range list {
			if u.ID == "" {
				return errorf("Microsoft Graph answered %s with a user that has no id, so Straza counts the read as incomplete.", what)
			}
			users = append(users, UserChange{
				ID: u.ID, Removed: present(u.Removed), Enabled: u.AccountEnabled,
				UPN: u.UPN, DisplayName: u.DisplayName, Mail: u.Mail, ParentID: u.IdentityParentID,
			})
		}
		return nil
	})
	if err != nil {
		return UsersResult{}, err
	}
	return UsersResult{Users: users, Link: next}, nil
}
