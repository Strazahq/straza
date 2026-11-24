package entragraph

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// nameTaken is the message of Graph's 400 for a uniqueName that a group
// already holds, lower-cased and without its full stop.
const nameTaken = "another object with the same value for property uniquename already exists"

// GroupSpec is the group EnsureGroup makes sure of. UniqueName is the key of
// the create and also its mailNickname, so it cannot change once the group
// exists.
type GroupSpec struct {
	UniqueName  string
	DisplayName string
	Description string
}

// Group is a security group as Graph answers it.
type Group struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	UniqueName  string `json:"uniqueName"`
}

// groupWrite is the body of the create-if-missing write: a plain security
// group with assigned members, owned by the service app.
type groupWrite struct {
	DisplayName     string   `json:"displayName"`
	Description     string   `json:"description"`
	MailEnabled     bool     `json:"mailEnabled"`
	MailNickname    string   `json:"mailNickname"`
	SecurityEnabled bool     `json:"securityEnabled"`
	GroupTypes      []string `json:"groupTypes"`
	Owners          []string `json:"owners@odata.bind"`
}

// EnsureGroup creates the security group keyed on spec.UniqueName, owned by
// the service app, unless Graph already holds one with that uniqueName. A
// 201 returns the group Graph created. A 204, which updated the group, and
// a 400 that says the uniqueName is taken both return the group read back
// by its uniqueName, so a repeated call never makes a second group. That
// read may answer ErrNotFound for some seconds after a create. After the
// 400 no write of Straza to the group has succeeded yet, so a caller that
// links only a group it could write follows with UpdateGroup. Any other
// answer is an error.
func (c *Client) EnsureGroup(ctx context.Context, spec GroupSpec) (Group, error) {
	if err := checkUniqueName(spec.UniqueName); err != nil {
		return Group{}, err
	}
	if spec.DisplayName == "" {
		return Group{}, errorf("Straza did not create the group %s, because it has no display name, which Graph requires.", spec.UniqueName)
	}
	_, owner, err := c.bearer(ctx)
	if err != nil {
		return Group{}, err
	}
	what := "the create of the group " + spec.UniqueName
	body := groupWrite{
		DisplayName:     spec.DisplayName,
		Description:     spec.Description,
		MailNickname:    spec.UniqueName,
		SecurityEnabled: true,
		GroupTypes:      []string{},
		Owners:          []string{c.base + "/v1.0/servicePrincipals/" + url.PathEscape(owner)},
	}
	ans, err := c.call(ctx, what, http.MethodPatch, c.uniqueNameURL(spec.UniqueName), body, "create-if-missing")
	var ge *Error
	switch {
	case errors.As(err, &ge) && ge.Status == http.StatusBadRequest && strings.Contains(strings.ToLower(ge.Message), nameTaken):
		return c.GroupByUniqueName(ctx, spec.UniqueName)
	case err != nil:
		return Group{}, err
	case ans.status == http.StatusNoContent:
		return c.GroupByUniqueName(ctx, spec.UniqueName)
	case ans.status == http.StatusCreated:
		return decodeGroup(what, ans.body)
	}
	return Group{}, unexpected(what, ans.status, "201 or 204")
}

// GroupByUniqueName reads the group that holds the uniqueName name. A 404
// matches ErrNotFound.
func (c *Client) GroupByUniqueName(ctx context.Context, name string) (Group, error) {
	if err := checkUniqueName(name); err != nil {
		return Group{}, err
	}
	what := "the read of the group " + name
	ans, err := c.call(ctx, what, http.MethodGet, c.uniqueNameURL(name)+"?$select=id,displayName,description,uniqueName", nil, "")
	if err != nil {
		return Group{}, err
	}
	if ans.status != http.StatusOK {
		return Group{}, unexpected(what, ans.status, "200")
	}
	return decodeGroup(what, ans.body)
}

// UpdateGroup writes the display name and the description of the group id.
// Graph answers 403 when the service app does not own the group.
func (c *Client) UpdateGroup(ctx context.Context, id, displayName, description string) error {
	if err := checkID(id); err != nil {
		return err
	}
	what := "the update of group " + id
	body := struct {
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
	}{displayName, description}
	return c.noContent(ctx, what, http.MethodPatch, c.groupURL(id), body)
}

// DeleteGroup deletes the group id. A 404 matches ErrNotFound.
func (c *Client) DeleteGroup(ctx context.Context, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	return c.noContent(ctx, "the delete of group "+id, http.MethodDelete, c.groupURL(id), nil)
}

// noContent sends a write whose only good answer is 204.
func (c *Client) noContent(ctx context.Context, what, method, target string, body any) error {
	ans, err := c.call(ctx, what, method, target, body, "")
	if err != nil {
		return err
	}
	if ans.status != http.StatusNoContent {
		return unexpected(what, ans.status, "204")
	}
	return nil
}

// uniqueNameURL addresses a group by its uniqueName, an OData alternate key.
func (c *Client) uniqueNameURL(name string) string {
	return c.base + "/v1.0/groups(uniqueName='" + url.PathEscape(name) + "')"
}

// groupURL addresses a group by its object ID.
func (c *Client) groupURL(id string) string {
	return c.base + "/v1.0/groups/" + url.PathEscape(id)
}

// decodeGroup reads a group from an answer's body.
func decodeGroup(what string, body []byte) (Group, error) {
	var g Group
	if json.Unmarshal(body, &g) != nil || g.ID == "" {
		return Group{}, errorf("Microsoft Graph answered %s without a group that has an id, so Straza counts the request as failed.", what)
	}
	return g, nil
}

// checkUniqueName refuses an empty uniqueName and one with a single quote,
// which would end the OData string inside the request's path.
func checkUniqueName(name string) error {
	if name == "" {
		return errorf("Straza sent no request for a group, because the group's uniqueName is empty.")
	}
	if strings.Contains(name, "'") {
		return errorf("Straza sent no request for the group %q, because its uniqueName holds a single quote, which Graph would read as the end of the name.", name)
	}
	return nil
}

// checkID refuses an empty object ID, which would address the collection of
// all groups.
func checkID(id string) error {
	if id == "" {
		return errorf("Straza sent no request for a group, because the group's ID is empty.")
	}
	return nil
}
