package entragraph

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

// deltaKinds maps the coarse types a delta read gives its members.
var deltaKinds = map[string]Kind{
	"#microsoft.graph.user":             KindUser,
	"#microsoft.graph.servicePrincipal": KindServicePrincipal,
	"#microsoft.graph.group":            KindGroup,
}

// MemberRef is a member as a delta read names it: an ID and a coarse kind.
type MemberRef struct {
	ID   string
	Kind Kind
}

// GroupChange is what a delta read says about one group. Removed says the
// group itself was deleted. Added lists the members that joined, and Gone
// the IDs of the members that left. The first delta read of a group lists
// all of its members as added.
type GroupChange struct {
	ID      string
	Removed bool
	Added   []MemberRef
	Gone    []string
}

// DeltaResult is one complete delta read: the changed groups in the order
// Graph first named them, and the delta link the next read continues from.
type DeltaResult struct {
	Groups []GroupChange
	Link   string
}

// Delta reads the membership changes of groups. With an empty link it
// starts a delta of the groups groupIDs names, and otherwise it continues
// from link, a delta link of an earlier result. It walks every page until
// one names the next delta link. A group that spans several pages is merged
// into one GroupChange. As with Members, any failure on any page returns an
// error and no partial result.
func (c *Client) Delta(ctx context.Context, link string, groupIDs []string) (DeltaResult, error) {
	what := "the delta read of the role groups"
	if link == "" {
		first, err := c.deltaStart(groupIDs)
		if err != nil {
			return DeltaResult{}, err
		}
		link = first
	} else if err := c.checkLink(what, link); err != nil {
		return DeltaResult{}, errorf("Straza did not follow the delta link it was handed, because it is not on %s. Start a fresh delta read with an empty link.", c.graph.Host)
	}
	var groups []GroupChange
	at := map[string]int{}
	next, err := c.walk(ctx, what, link, true, func(value json.RawMessage) error {
		var list []struct {
			ID      string          `json:"id"`
			Removed json.RawMessage `json:"@removed"`
			Members []struct {
				Type    string          `json:"@odata.type"`
				ID      string          `json:"id"`
				Removed json.RawMessage `json:"@removed"`
			} `json:"members@delta"`
		}
		if err := json.Unmarshal(value, &list); err != nil {
			return errorf("Microsoft Graph answered %s with a list Straza cannot read, so Straza counts the read as incomplete.", what)
		}
		for _, g := range list {
			if g.ID == "" {
				return errorf("Microsoft Graph answered %s with a group that has no id, so Straza counts the read as incomplete.", what)
			}
			i, seen := at[g.ID]
			if !seen {
				i = len(groups)
				at[g.ID] = i
				groups = append(groups, GroupChange{ID: g.ID})
			}
			change := &groups[i]
			change.Removed = change.Removed || present(g.Removed)
			for _, m := range g.Members {
				if m.ID == "" {
					return errorf("Microsoft Graph answered %s with a member of group %s that has no id, so Straza counts the read as incomplete.", what, g.ID)
				}
				if present(m.Removed) {
					change.Gone = append(change.Gone, m.ID)
					continue
				}
				kind, ok := deltaKinds[m.Type]
				if !ok {
					kind = KindOther
				}
				change.Added = append(change.Added, MemberRef{ID: m.ID, Kind: kind})
			}
		}
		return nil
	})
	if err != nil {
		return DeltaResult{}, err
	}
	return DeltaResult{Groups: groups, Link: next}, nil
}

// deltaStart is the first link of a delta of the groups ids names, which
// asks for their members only. It refuses an empty list, because a delta
// with no filter walks every group of the tenant.
func (c *Client) deltaStart(ids []string) (string, error) {
	if len(ids) == 0 {
		return "", errorf("Straza sent no delta read, because it names no group, and Straza never reads the delta of every group in the tenant.")
	}
	terms := make([]string, len(ids))
	for i, id := range ids {
		if err := checkID(id); err != nil {
			return "", err
		}
		if strings.Contains(id, "'") {
			return "", errorf("Straza sent no delta read, because the group ID %q holds a single quote, which Graph would read as the end of the ID.", id)
		}
		terms[i] = "id eq '" + id + "'"
	}
	filter := strings.ReplaceAll(url.QueryEscape(strings.Join(terms, " or ")), "+", "%20")
	return c.base + "/v1.0/groups/delta?$select=members&$filter=" + filter, nil
}

// present says whether an @removed annotation was given a value.
func present(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}
