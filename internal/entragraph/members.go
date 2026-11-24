package entragraph

import (
	"context"
	"encoding/json"
)

// Kind is the type of a group member, from its @odata.type.
type Kind string

// The kinds a member read and a delta read report. A member read tells a
// person (user) from an agent user and a nested group. AgentIdentities and
// the sponsors that Agents reads tell an agent identity from another
// service principal. A delta read gives coarse types only: an agent user
// comes as user and an agent identity as servicePrincipal. Every other type
// is other.
const (
	KindUser             Kind = "user"
	KindAgentUser        Kind = "agentUser"
	KindGroup            Kind = "group"
	KindServicePrincipal Kind = "servicePrincipal"
	KindAgentIdentity    Kind = "agentIdentity"
	KindOther            Kind = "other"
)

// memberKinds maps the types a member read tells apart.
var memberKinds = map[string]Kind{
	"#microsoft.graph.user":      KindUser,
	"#microsoft.graph.agentUser": KindAgentUser,
	"#microsoft.graph.group":     KindGroup,
}

// servicePrincipalKinds maps the types the read of a group's service
// principals tells apart. Every other entry of that read is a service
// principal, whatever type it names.
var servicePrincipalKinds = map[string]Kind{
	"#microsoft.graph.agentIdentity": KindAgentIdentity,
}

// Member is one member of a group as a member read lists it. ParentID is the
// agent identity behind an agent user, which Graph names only in a read that
// sends no $select. Members and TransitiveMembers send one, and Graph then
// leaves identityParent out and answers 500 when the $select asks for it, so
// ParentID is empty on their reads, Kind is what marks an agent user, and
// AgentUserParents is the read that names the parents. Enabled is
// accountEnabled when Graph returned a value for it, and nil otherwise,
// which is what a service app without User.Read.All gets. UPN, DisplayName
// and Mail are the names of a user, each empty when Graph left it out.
type Member struct {
	ID          string
	Kind        Kind
	ParentID    string
	Enabled     *bool
	UPN         string
	DisplayName string
	Mail        string
}

// Members reads the direct members of the group id, every page to the end.
// It returns members only when every page was read, and on any failure an
// error and no members, because a caller revokes whoever a returned list
// leaves out. A complete read of an empty group is an empty, non-nil list.
func (c *Client) Members(ctx context.Context, id string) ([]Member, error) {
	return c.members(ctx, id, "members?$select="+userFields, "the read of the members of group ", memberKinds, KindOther)
}

// TransitiveMembers reads the members of the group id and of every group
// nested in it, under the same rule as Members.
func (c *Client) TransitiveMembers(ctx context.Context, id string) ([]Member, error) {
	return c.members(ctx, id, "transitiveMembers?$select="+userFields, "the read of the transitive members of group ", memberKinds, KindOther)
}

// AgentIdentities reads the service principals that are direct members of
// the group groupID, under the same rule as Members. It is the only v1.0
// member read that lists an agent identity, and it lists one only on the
// group the agent identity is a direct member of. No transitive read does.
// Kind is KindAgentIdentity for an agent identity and KindServicePrincipal
// for any other service principal. Each member has its ID, DisplayName and
// Enabled, which is nil when Graph left accountEnabled out.
func (c *Client) AgentIdentities(ctx context.Context, groupID string) ([]Member, error) {
	return c.members(ctx, groupID, "members/microsoft.graph.servicePrincipal", "the read of the service principals in group ", servicePrincipalKinds, KindServicePrincipal)
}

// AgentUserParents reads the members of the group groupID and of every
// group nested in it with no $select, the one member read in which Graph
// names the identityParent of an agent user, under the same rule as
// Members. It maps the ID of each agent user to the ID of the agent
// identity behind it, both as Graph wrote them, and leaves out an agent
// user that names no parent. A complete read without an agent user is an
// empty, non-nil map.
func (c *Client) AgentUserParents(ctx context.Context, groupID string) (map[string]string, error) {
	list, err := c.members(ctx, groupID, "transitiveMembers", "the read of the parents of the agent users in group ", memberKinds, KindOther)
	if err != nil {
		return nil, err
	}
	parents := map[string]string{}
	for _, m := range list {
		if m.Kind == KindAgentUser && m.ParentID != "" {
			parents[m.ID] = m.ParentID
		}
	}
	return parents, nil
}

// members walks one member collection of the group id. collection is the
// collection and its query after the group's address. kinds maps the types
// the read tells apart, and every other type is fallback.
func (c *Client) members(ctx context.Context, id, collection, what string, kinds map[string]Kind, fallback Kind) ([]Member, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	what += id
	out := []Member{}
	_, err := c.walk(ctx, what, c.groupURL(id)+"/"+collection, false, func(value json.RawMessage) error {
		var list []struct {
			Type           string `json:"@odata.type"`
			ID             string `json:"id"`
			AccountEnabled *bool  `json:"accountEnabled"`
			UPN            string `json:"userPrincipalName"`
			DisplayName    string `json:"displayName"`
			Mail           string `json:"mail"`
			IdentityParent *struct {
				ID string `json:"id"`
			} `json:"identityParent"`
		}
		if err := json.Unmarshal(value, &list); err != nil {
			return errorf("Microsoft Graph answered %s with a list Straza cannot read, so Straza counts the read as incomplete.", what)
		}
		for _, m := range list {
			if m.ID == "" {
				return errorf("Microsoft Graph answered %s with a member that has no id, so Straza counts the read as incomplete.", what)
			}
			kind, ok := kinds[m.Type]
			if !ok {
				kind = fallback
			}
			member := Member{ID: m.ID, Kind: kind, Enabled: m.AccountEnabled, UPN: m.UPN, DisplayName: m.DisplayName, Mail: m.Mail}
			if kind == KindAgentUser && m.IdentityParent != nil {
				member.ParentID = m.IdentityParent.ID
			}
			out = append(out, member)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
