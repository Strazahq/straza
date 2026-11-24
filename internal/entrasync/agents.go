package entrasync

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/entragraph"
	"github.com/strazahq/straza/internal/store"
)

// The reasons on the records of what the sync does to an agent.
const (
	reasonAgentCreated     = "an agent identity in a role's group in Entra"
	reasonAgentTakenOver   = "Entra owns agents, and the agent carries this Entra agent identity's ID"
	reasonAgentChanged     = "the agent identity changed in Entra"
	reasonSponsorChanged   = "the agent's sponsor changed in Entra"
	reasonIdentityDisabled = "the agent identity is disabled in Entra"
	reasonIdentityDeleted  = "the agent identity was deleted in Entra"
	reasonAgentUserOff     = "the agent user is disabled in Entra"
)

// reasonAgentClash is the reason on the record of an agent created under
// clashName, formatted with the display name another user has.
const reasonAgentClash = reasonAgentCreated + ". Another Straza user has the name %s, so this agent carries the start of its Entra agent ID."

// The sentences on the record of an agent that has no single sponsor.
var sponsorSentence = map[string]string{
	store.EntraSponsorNone:      "Entra lists no sponsor for this agent. Add one person as its sponsor in Entra.",
	store.EntraSponsorSeveral:   "Entra lists more than one sponsor for this agent, and Straza takes a sponsor only when there is exactly one person. Leave one in Entra.",
	store.EntraSponsorNotPerson: "Entra lists a sponsor that is no person, and Straza takes a sponsor only when it is one person. Name one person in Entra.",
	store.EntraSponsorNoUser:    "Entra lists a sponsor that is no Straza user. Give that person a role in Straza, or name another sponsor in Entra.",
}

// noAgentState is the sentence of a read of an agent that came back
// without its enabled state.
const noAgentState = "Microsoft Graph answered the read of agent identity %s without its enabled state, so Straza counts the read as incomplete. Grant the service app the application permission AgentIdentity.Read.All and give admin consent."

// agentKindNHI is the attrs of an agent the sync creates, the mark every
// other writer of an agent sets.
const agentKindNHI = `{"kind":"nhi"}`

// readsAgentUsers reports whether Entra owns agent users in this deployment.
func (s *Syncer) readsAgentUsers() bool {
	return slices.Contains(s.d.Config.Read, config.EntraReadAgentUsers)
}

// readsAgentIdentities reports whether Entra owns agent identities in this
// deployment.
func (s *Syncer) readsAgentIdentities() bool {
	return slices.Contains(s.d.Config.Read, config.EntraReadAgentIdentities)
}

// agentUser is what a read says about an agent's agent user: its Entra
// object id, and state, a store word, or empty when Entra lists it no
// longer.
type agentUser struct {
	id, state string
}

// objectState is the word the store keeps for an enabled state.
func objectState(enabled bool) string {
	if enabled {
		return store.EntraObjectEnabled
	}
	return store.EntraObjectDisabled
}

// readAgents reads from Entra every agent identity of ids that this pass
// has not read yet and keeps the answers for the rest of the pass.
func (s *Syncer) readAgents(ctx context.Context, ids []string) error {
	var ask []string
	for _, id := range ids {
		if _, ok := s.seen[id]; !ok && !slices.Contains(ask, id) {
			ask = append(ask, id)
		}
	}
	if len(ask) == 0 {
		return nil
	}
	got, err := s.d.Graph.Agents(ctx, ask)
	if err != nil {
		return err
	}
	for id, a := range got {
		s.seen[id] = a
	}
	return nil
}

// refreshAgents reads every agent the sync owns and applies what Entra
// says now, so a disabled, deleted or renamed agent identity and a changed
// sponsor are seen in every pass, whether or not a group changed.
func (s *Syncer) refreshAgents(ctx context.Context) error {
	if !s.readsAgentUsers() && !s.readsAgentIdentities() {
		return nil
	}
	rows, err := s.d.Agents.List(ctx)
	if err != nil {
		return err
	}
	var ids []string
	for _, row := range rows {
		u, err := s.d.Users.GetByID(ctx, row.UserID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if u.Origin == store.OriginEntra && u.EntraAgentID != "" {
			ids = append(ids, u.EntraAgentID)
		}
	}
	if err := s.readAgents(ctx, ids); err != nil {
		return err
	}
	for _, id := range ids {
		if _, _, err := s.agent(ctx, id, nil, false); err != nil {
			return err
		}
	}
	return nil
}

// sponsor answers the Straza username of the agent's sponsor when Entra
// lists exactly one sponsor and that sponsor is a person Straza has by the
// Entra object id. Otherwise it answers no username, the store's word for
// why, and what Entra lists.
func (s *Syncer) sponsor(ctx context.Context, a entragraph.Agent) (string, string, []store.EntraSponsor, error) {
	listed := make([]store.EntraSponsor, 0, len(a.Sponsors))
	for _, sp := range a.Sponsors {
		listed = append(listed, store.EntraSponsor{ID: sp.ID, Kind: string(sp.Kind)})
	}
	switch {
	case len(a.Sponsors) == 0:
		return "", store.EntraSponsorNone, nil, nil
	case len(a.Sponsors) > 1:
		return "", store.EntraSponsorSeveral, listed, nil
	case a.Sponsors[0].Kind != entragraph.KindUser:
		return "", store.EntraSponsorNotPerson, listed, nil
	}
	u, err := s.d.Users.GetByExternalID(ctx, a.Sponsors[0].ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "", store.EntraSponsorNoUser, listed, nil
	case err != nil:
		return "", "", nil, err
	case u.IsNHI():
		return "", store.EntraSponsorNotPerson, listed, nil
	}
	return u.Username, "", nil, nil
}

// agent makes the Straza agent of the Entra agent identity id match what
// this pass read of it, and answers that user. It finds the agent by the
// Entra agent id alone. An agent of another origin is taken over. With
// create, an identity that has no agent gets one, named as agentName says.
// au is the agent's agent user when the caller read it, and nil otherwise.
// An identity Entra did not answer for is ReasonUnread and is never written,
// and an identity that is gone disables an agent only after an earlier good
// read of it.
func (s *Syncer) agent(ctx context.Context, id string, au *agentUser, create bool) (store.User, string, error) {
	a, ok := s.seen[id]
	if !ok {
		return store.User{}, ReasonUnread, nil
	}
	if !a.Missing && a.Enabled == nil {
		return store.User{}, "", sentence(fmt.Sprintf(noAgentState, id))
	}
	u, err := s.d.Users.GetByEntraAgentID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		if !create {
			return store.User{}, "", nil
		}
		return s.createAgent(ctx, a, au)
	}
	if err != nil {
		return store.User{}, "", err
	}
	protected := ""
	if s.d.Protected != nil {
		protected = s.d.Protected(ctx)
	}
	if u.ID == protected || !u.IsNHI() {
		return u, "", nil
	}
	row, err := s.d.Agents.Get(ctx, u.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.User{}, "", err
	}
	if a.Missing && row.Identity == "" {
		return store.User{}, ReasonUnread, nil
	}
	if u.Origin != store.OriginEntra {
		// The agent keeps its external id. The link to Entra is the Entra
		// agent id, and a second id written here could meet another row's.
		if u, err = s.d.Users.TakeOver(ctx, u.ID, u.ExternalID); err != nil {
			return store.User{}, "", err
		}
		s.d.People.Updated(ctx, u, []string{"origin"}, reasonAgentTakenOver)
	}
	old := row
	row.UserID = u.ID
	if au != nil {
		row.AgentUser, row.AgentUserID = au.state, au.id
	}
	if !s.readsAgentUsers() {
		// A kind the deployment no longer takes from Entra is ignored, so
		// an agent user's last state does not keep the agent disabled.
		row.AgentUser, row.AgentUserID = "", ""
	}
	if a.Missing {
		row.Identity = store.EntraObjectDisabled
	} else {
		row.Identity = objectState(*a.Enabled)
		if u, err = s.mirrorAgent(ctx, u, a, &row); err != nil {
			return store.User{}, "", err
		}
	}
	if err := s.keep(ctx, u, old, row); err != nil {
		return store.User{}, "", err
	}
	why := reasonIdentityDisabled
	if a.Missing {
		why = reasonIdentityDeleted
	}
	u, err = s.settle(ctx, u, row, why)
	return u, "", err
}

// agentName answers the sign-in name of the agent of the agent identity a:
// its display name in Entra, or, when another Straza user has that name,
// the display name with the start of the agent identity's id. Entra allows
// many agent identities with one display name, so the second one still
// gets an agent, and it takes the plain name once that is free. self is
// the agent's own user id, or empty before it exists.
func (s *Syncer) agentName(ctx context.Context, a entragraph.Agent, self string) (string, error) {
	holder, err := s.d.Users.GetByUsername(ctx, a.DisplayName)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return a.DisplayName, nil
	case err != nil:
		return "", err
	case holder.ID == self:
		return a.DisplayName, nil
	}
	return clashName(a), nil
}

// clashName is the display name of the agent identity a with the first
// eight characters of its id.
func clashName(a entragraph.Agent) string {
	return a.DisplayName + " (" + a.ID[:min(8, len(a.ID))] + ")"
}

// createAgent makes the Straza agent of an agent identity that has none.
func (s *Syncer) createAgent(ctx context.Context, a entragraph.Agent, au *agentUser) (store.User, string, error) {
	if a.Missing || a.DisplayName == "" {
		return store.User{}, ReasonUnread, nil
	}
	sponsor, state, listed, err := s.sponsor(ctx, a)
	if err != nil {
		return store.User{}, "", err
	}
	row := store.EntraAgent{Identity: objectState(*a.Enabled), SponsorState: state, Sponsors: listed}
	if au != nil {
		row.AgentUser, row.AgentUserID = au.state, au.id
	}
	status := store.UserActive
	if !row.Active() {
		status = store.UserDisabled
	}
	name, err := s.agentName(ctx, a, "")
	if err != nil {
		return store.User{}, "", err
	}
	agent := store.User{
		Username: name, Display: a.DisplayName, EntraAgentID: a.ID, Sponsor: sponsor,
		UserType: store.UserTypeAgent, Attrs: agentKindNHI, Origin: store.OriginEntra, Status: status,
	}
	u, err := s.d.Users.Create(ctx, agent)
	if errors.Is(err, store.ErrConflict) && name != clashName(a) {
		// A deleted user still holds the plain name under the unique key.
		agent.Username = clashName(a)
		u, err = s.d.Users.Create(ctx, agent)
	}
	if errors.Is(err, store.ErrConflict) {
		return store.User{}, ReasonNameTaken, nil
	}
	if err != nil {
		return store.User{}, "", err
	}
	reason := reasonAgentCreated
	if u.Username != a.DisplayName {
		reason = fmt.Sprintf(reasonAgentClash, a.DisplayName)
	}
	s.d.People.Created(ctx, u, reason)
	row.UserID = u.ID
	return u, "", s.keep(ctx, u, store.EntraAgent{}, row)
}

// mirrorAgent writes the agent identity's name and sponsor onto its agent
// u and the sponsor's state into row. The sign-in name is the one
// agentName answers, so a name another Straza user has is never taken.
func (s *Syncer) mirrorAgent(ctx context.Context, u store.User, a entragraph.Agent, row *store.EntraAgent) (store.User, error) {
	if a.DisplayName != "" {
		name, err := s.agentName(ctx, a, u.ID)
		if err != nil {
			return store.User{}, err
		}
		if u.Username != name {
			renamed, moved, err := s.d.Users.Rename(ctx, u.ID, name)
			if errors.Is(err, store.ErrConflict) && name != clashName(a) && u.Username != clashName(a) {
				// A deleted user still holds the plain name under the unique key.
				renamed, moved, err = s.d.Users.Rename(ctx, u.ID, clashName(a))
			}
			switch {
			case errors.Is(err, store.ErrConflict):
				s.d.Log.Warn("entra sync: an agent's new name from Entra was not written, because another Straza user has it", "user", u.ID, "name", name)
			case err != nil:
				return store.User{}, err
			default:
				u = renamed
				s.d.People.Updated(ctx, u, []string{"username"}, reasonAgentChanged)
				if err := s.sponsorMoved(ctx, moved); err != nil {
					return store.User{}, err
				}
			}
		}
	}
	if a.DisplayName != "" && u.Display != a.DisplayName {
		var err error
		if u, err = s.d.Users.UpdateFields(ctx, u.ID, store.UserFields{Display: &a.DisplayName}, nil); err != nil {
			return store.User{}, err
		}
		s.d.People.Updated(ctx, u, []string{"display"}, reasonAgentChanged)
	}
	sponsor, state, listed, err := s.sponsor(ctx, a)
	if err != nil {
		return store.User{}, err
	}
	row.SponsorState, row.Sponsors = state, listed
	if u.Sponsor != sponsor {
		if u, err = s.d.Users.UpdateFields(ctx, u.ID, store.UserFields{Sponsor: &sponsor}, nil); err != nil {
			return store.User{}, err
		}
		s.d.People.Updated(ctx, u, []string{"sponsor"}, reasonSponsorChanged)
	}
	return u, nil
}

// keep stores row when it differs from old and writes one record when the
// agent u enters the state of having no single sponsor and one when it
// leaves it, so a pass that finds the state unchanged writes nothing.
func (s *Syncer) keep(ctx context.Context, u store.User, old, row store.EntraAgent) error {
	if old.UserID == row.UserID && old.Identity == row.Identity && old.AgentUser == row.AgentUser &&
		old.AgentUserID == row.AgentUserID && old.SponsorState == row.SponsorState && slices.Equal(old.Sponsors, row.Sponsors) {
		return nil
	}
	if err := s.d.Agents.Put(ctx, row); err != nil {
		return err
	}
	data := map[string]any{"user": u.ID, "username": u.Username, "entraAgentId": u.EntraAgentID}
	switch {
	case old.SponsorState == "" && row.SponsorState != "":
		data["action"], data["state"], data["reason"] = "entra.sponsor.unresolved", row.SponsorState, sponsorSentence[row.SponsorState]
		if len(row.Sponsors) > 0 {
			data["sponsors"] = row.Sponsors
		}
		s.d.Record(ctx, data)
	case old.SponsorState != "" && row.SponsorState == "":
		data["action"], data["sponsor"] = "entra.sponsor.resolved", u.Sponsor
		s.d.Record(ctx, data)
	}
	return nil
}

// settle disables the agent u when Entra no longer lets it act and enables
// it when Entra does again. why is the reason for a disable that the agent
// identity causes.
func (s *Syncer) settle(ctx context.Context, u store.User, row store.EntraAgent, why string) (store.User, error) {
	switch {
	case !row.Active() && u.Status == store.UserActive:
		if row.Identity != store.EntraObjectDisabled {
			why = reasonAgentUserOff
		}
		return s.d.People.Disable(ctx, u.ID, why)
	case row.Active() && u.Status != store.UserActive:
		return s.d.People.Enable(ctx, u.ID)
	}
	return u, nil
}

// agentUserChanged applies one entry of the users feed about an agent
// user to its agent and answers whether the entry was about one. An entry
// that names a parent belongs to the agent of that agent identity. A
// deleted account comes as its id alone, so it is found by the agent user
// id the sync kept, and it then counts as no longer listed, because the
// agent identity is what acts. An agent Straza does not own is left alone,
// and an entry without an enabled state changes nothing.
func (s *Syncer) agentUserChanged(ctx context.Context, c entragraph.UserChange) (bool, error) {
	if c.ParentID == "" && !c.Removed {
		return false, nil
	}
	if !s.readsAgentUsers() {
		return c.ParentID != "", nil
	}
	var row store.EntraAgent
	var err error
	if c.ParentID == "" {
		if row, err = s.d.Agents.GetByAgentUser(ctx, c.ID); errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
	} else {
		id, ok := store.NormalizeEntraAgentID(c.ParentID)
		if !ok || (c.Enabled == nil && !c.Removed) {
			return true, nil
		}
		u, uerr := s.d.Users.GetByEntraAgentID(ctx, id)
		if errors.Is(uerr, store.ErrNotFound) {
			return true, nil
		}
		if uerr != nil {
			return true, uerr
		}
		if row, err = s.d.Agents.Get(ctx, u.ID); errors.Is(err, store.ErrNotFound) {
			return true, nil
		}
	}
	if err != nil {
		return true, err
	}
	u, err := s.d.Users.GetByID(ctx, row.UserID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.Origin != store.OriginEntra) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	old := row
	row.AgentUser, row.AgentUserID = "", ""
	if !c.Removed {
		row.AgentUser, row.AgentUserID = objectState(*c.Enabled), c.ID
	}
	if err := s.keep(ctx, u, old, row); err != nil {
		return true, err
	}
	_, err = s.settle(ctx, u, row, reasonIdentityDisabled)
	return true, err
}

// agentMembers finds the agents among the members of a complete read of
// the group groupID and gives each the role in want, or lists it in the
// answer with the reason it is not applied. agentUsers are the agent users
// the member read listed and nested the groups nested in the group. An
// agent identity is listed only by the group it is a direct member of, so
// each nested group is asked on its own.
func (s *Syncer) agentMembers(ctx context.Context, groupID string, agentUsers []entragraph.Member, nested []string, want map[string]bool) ([]NotApplied, error) {
	type member struct {
		m       entragraph.Member
		agentID string
		au      *agentUser
	}
	var found []member
	var skipped []NotApplied
	skip := func(m entragraph.Member, reason string) {
		skipped = append(skipped, NotApplied{ID: m.ID, Kind: string(m.Kind), Reason: reason})
	}
	if len(agentUsers) > 0 && !s.readsAgentUsers() {
		for _, m := range agentUsers {
			skip(m, ReasonNotRead)
		}
	} else if len(agentUsers) > 0 {
		parents, err := s.d.Graph.AgentUserParents(ctx, groupID)
		if err != nil {
			return nil, err
		}
		for _, m := range agentUsers {
			// An agent user whose enabled state did not come back fails the
			// read, as a person's does, so a disabled one is never missed.
			if m.Enabled == nil {
				return nil, sentence(fmt.Sprintf(noEnabledState, groupID))
			}
			id, ok := store.NormalizeEntraAgentID(parents[m.ID])
			if !ok {
				skip(m, ReasonUnread)
				continue
			}
			found = append(found, member{m: m, agentID: id, au: &agentUser{id: m.ID, state: objectState(*m.Enabled)}})
		}
	}
	if s.readsAgentIdentities() {
		listed := map[string]bool{}
		for i, gid := range append([]string{groupID}, nested...) {
			list, err := s.d.Graph.AgentIdentities(ctx, gid)
			if i > 0 && errors.Is(err, entragraph.ErrNotFound) {
				continue // a nested group that is gone has no members
			}
			if err != nil {
				return nil, err
			}
			for _, m := range list {
				id, ok := store.NormalizeEntraAgentID(m.ID)
				switch {
				case listed[m.ID]:
				case m.Kind != entragraph.KindAgentIdentity || !ok:
					skip(m, ReasonNotAgent)
				default:
					found = append(found, member{m: m, agentID: id})
				}
				listed[m.ID] = true
			}
		}
	}
	ids := make([]string, 0, len(found))
	for _, f := range found {
		ids = append(ids, f.agentID)
	}
	if err := s.readAgents(ctx, ids); err != nil {
		return nil, err
	}
	protected := ""
	if s.d.Protected != nil {
		protected = s.d.Protected(ctx)
	}
	for _, f := range found {
		u, why, err := s.agent(ctx, f.agentID, f.au, true)
		switch {
		case err != nil:
			return nil, err
		case why != "":
			skip(f.m, why)
		case u.ID != protected:
			want[u.ID] = true
		}
	}
	return skipped, nil
}
