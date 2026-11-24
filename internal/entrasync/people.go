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

// People is how the sync reports a person it wrote and asks for a person
// to be disabled or enabled, so that these run the same audit and kill
// switch path as for every identity source.
type People struct {
	// Created reports a person or an agent the sync created, with the
	// reason.
	Created func(ctx context.Context, u store.User, reason string)
	// Updated reports the fields the sync changed on u, with the reason.
	Updated func(ctx context.Context, u store.User, changed []string, reason string)
	// Disable disables the user and ends their sessions.
	Disable func(ctx context.Context, userID, reason string) (store.User, error)
	// Enable enables the user and lifts the revocation a Disable wrote. A
	// lock an admin set stays.
	Enable func(ctx context.Context, userID string) (store.User, error)
}

// The reasons on the records of what the sync does to a person.
const (
	reasonCreated      = "a member of a role's group in Entra"
	reasonTakenOver    = "Entra owns people, and the account carries this person's Entra object ID"
	reasonLinkedByName = "Entra owns people, and the account has the sign-in name of this Entra account"
	reasonChanged      = "the account changed in Entra"
	reasonDisabled     = "the account is disabled in Entra"
	reasonDeleted      = "the account was deleted in Entra"
	reasonNameFreed    = "another Entra account now has this sign-in name"
	reasonSponsorMoved = "the sponsor's sign-in name changed"
)

// leftSuffix marks the sign-in name of a disabled person whose name
// another Entra account now has.
const leftSuffix = " (left)"

// usersLinkKey is the settings key of the delta link of the users feed. It
// is stored so that a restart or another replica continues the feed and
// misses no change.
const usersLinkKey = "entra.users.link"

// noEnabledState is the sentence of a member read that came back without
// the accounts' enabled state.
const noEnabledState = "Microsoft Graph answered the read of the members of group %s without the accounts' enabled state, so Straza counts the read as incomplete. Grant the service app the application permission User.Read.All and give admin consent."

// feedRefused is the sentence of a read of the users feed that Graph
// refused, formatted with Graph's answer.
const feedRefused = "Microsoft Graph refused the read of the changed users, so Straza cannot tell which accounts were disabled or deleted. Grant the service app the application permission User.Read.All and give admin consent. Graph answered: %s"

// sentence is a pass's failure in the words an admin reads on the state of
// the read: what failed, why, and what to do.
type sentence string

func (s sentence) Error() string { return string(s) }

// account is what Entra says about one person.
type account struct {
	id, upn, display, mail string
	enabled, removed       bool
}

// readsPeople reports whether Entra owns people in this deployment.
func (s *Syncer) readsPeople() bool {
	return slices.Contains(s.d.Config.Read, config.EntraReadPeople)
}

// person makes the Straza user of the Entra account a match what Entra
// says and answers that user. It finds the user by the Entra object id. A
// user of another origin is taken over. With create, an account that has no
// user gets one, or is linked to the user of its sign-in name when linkable
// allows that, and otherwise the answer is ReasonNameTaken. Without create
// such an account answers the zero user. The break-glass account, an agent
// and a service are answered as they are and never written.
func (s *Syncer) person(ctx context.Context, a account, create bool) (store.User, string, error) {
	protected := ""
	if s.d.Protected != nil {
		protected = s.d.Protected(ctx)
	}
	u, err := s.d.Users.GetByExternalID(ctx, a.id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if !create {
			return store.User{}, "", nil
		}
		status := store.UserActive
		if !a.enabled {
			status = store.UserDisabled
		}
		u, err = s.d.Users.Create(ctx, store.User{
			Username: a.upn, ExternalID: a.id, Email: a.mail, Display: a.display,
			UserType: store.UserTypeHuman, Origin: store.OriginEntra, Status: status,
		})
		if err == nil {
			s.d.People.Created(ctx, u, reasonCreated)
			return u, "", nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return store.User{}, "", err
		}
		named, err := s.d.Users.GetByUsername(ctx, a.upn)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return store.User{}, ReasonNameTaken, nil
		case err != nil:
			return store.User{}, "", err
		case linkable(named, protected):
			if u, err = s.d.Users.TakeOver(ctx, named.ID, a.id); err != nil {
				return store.User{}, "", err
			}
			s.d.People.Updated(ctx, u, []string{"external_id", "origin"}, reasonLinkedByName)
		case left(named, a.id, protected):
			if err := s.freeName(ctx, named); err != nil {
				return store.User{}, "", err
			}
			return s.person(ctx, a, create)
		default:
			return store.User{}, ReasonNameTaken, nil
		}
	case err != nil:
		return store.User{}, "", err
	case u.ID == protected || u.IsNHI():
		return u, "", nil
	case u.Origin != store.OriginEntra:
		if u, err = s.d.Users.TakeOver(ctx, u.ID, a.id); err != nil {
			return store.User{}, "", err
		}
		s.d.People.Updated(ctx, u, []string{"origin"}, reasonTakenOver)
	}
	u, err = s.mirror(ctx, u, a)
	return u, "", err
}

// linkable reports whether the user u may be linked to an Entra account
// that has its sign-in name: an active person that Entra does not own yet
// and that holds no Entra object id. A disabled account is never linked by
// a name, so a newcomer with a leaver's name does not bring it back.
func linkable(u store.User, protected string) bool {
	return u.Status == store.UserActive && u.Origin != store.OriginEntra && !u.IsNHI() &&
		u.ID != protected && !isObjectID(u.ExternalID)
}

// left reports whether the user u is a person who left: a disabled person
// that Entra owns, under another object id than the account id that now
// has u's sign-in name. Entra gives one sign-in name to one account, so
// that account's having the name proves that u's account lost it.
func left(u store.User, id, protected string) bool {
	return u.Status != store.UserActive && u.Origin == store.OriginEntra && !u.IsNHI() &&
		u.ID != protected && u.ExternalID != id
}

// freeName renames the person u, who left, so that the account that now
// has the sign-in name can get its own user. The new name is the old one
// with leftSuffix, and with the start of u's object id as well when that
// name is taken too. The agents u sponsors follow u to the new name, so
// they never pass to the person who gets the old one.
func (s *Syncer) freeName(ctx context.Context, u store.User) error {
	name := u.Username + leftSuffix
	renamed, agents, err := s.d.Users.Rename(ctx, u.ID, name)
	if errors.Is(err, store.ErrConflict) {
		name = u.Username + " (left " + u.ExternalID[:min(8, len(u.ExternalID))] + ")"
		renamed, agents, err = s.d.Users.Rename(ctx, u.ID, name)
	}
	if err != nil {
		return err
	}
	s.d.People.Updated(ctx, renamed, []string{"username"}, reasonNameFreed)
	return s.sponsorMoved(ctx, agents)
}

// sponsorMoved reports each of the users ids whose sponsor name a rename
// changed.
func (s *Syncer) sponsorMoved(ctx context.Context, ids []string) error {
	for _, id := range ids {
		agent, err := s.d.Users.GetByID(ctx, id)
		if err != nil {
			return err
		}
		s.d.People.Updated(ctx, agent, []string{"sponsor"}, reasonSponsorMoved)
	}
	return nil
}

// isObjectID reports whether s has the form of an Entra object id, a GUID.
func isObjectID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F'):
			return false
		}
	}
	return true
}

// mirror writes what Entra says about the account onto its user u: the
// three names when Entra gave them, and the status. A deleted account
// disables the user and keeps the record.
func (s *Syncer) mirror(ctx context.Context, u store.User, a account) (store.User, error) {
	if a.upn != "" {
		var changed, agents []string
		if u.Username != a.upn {
			// The rename is its own write, so the agents the person sponsors
			// follow the new name. When another Straza user has that name,
			// the old one stays and the other names are still written.
			renamed, moved, err := s.d.Users.Rename(ctx, u.ID, a.upn)
			switch {
			case errors.Is(err, store.ErrConflict):
				s.d.Log.Warn("entra sync: a person's new sign-in name from Entra was not written, because another Straza user has it", "user", u.ID, "name", a.upn)
			case err != nil:
				return store.User{}, err
			default:
				u, agents, changed = renamed, moved, append(changed, "username")
			}
		}
		names := u
		for _, f := range []struct {
			name string
			have *string
			want string
		}{{"display", &names.Display, a.display}, {"email", &names.Email, a.mail}} {
			if *f.have != f.want {
				*f.have = f.want
				changed = append(changed, f.name)
			}
		}
		if names != u {
			var err error
			if u, err = s.d.Users.Update(ctx, names); err != nil {
				return store.User{}, err
			}
		}
		if len(changed) > 0 {
			s.d.People.Updated(ctx, u, changed, reasonChanged)
		}
		if err := s.sponsorMoved(ctx, agents); err != nil {
			return store.User{}, err
		}
	}
	switch {
	case (a.removed || !a.enabled) && u.Status == store.UserActive:
		reason := reasonDisabled
		if a.removed {
			reason = reasonDeleted
		}
		return s.d.People.Disable(ctx, u.ID, reason)
	case !a.removed && a.enabled && u.Status != store.UserActive:
		return s.d.People.Enable(ctx, u.ID)
	}
	return u, nil
}

// feed asks Entra which users changed since the last pass and applies each
// change to the Straza person of that account, or to the agent of an agent
// user. An account Straza has no user for is dropped. The first read starts
// from now and lists nobody, because the reads of the groups carry the
// state of every member. The link is stored only after every change was
// applied, so a failed pass reads the same changes again.
func (s *Syncer) feed(ctx context.Context) error {
	if !s.readsPeople() && !s.readsAgentUsers() {
		return nil
	}
	link, err := s.d.Settings.Get(ctx, usersLinkKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	res, err := s.d.Graph.UsersDelta(ctx, link)
	var ge *entragraph.Error
	if link != "" && errors.As(err, &ge) && (ge.Status == 400 || ge.Status == 404 || ge.Status == 410) {
		s.d.Log.Warn("entra sync: Entra no longer knows the link of the users feed, so the feed starts from now and the reads of the groups carry the members' state", "err", err)
		res, err = s.d.Graph.UsersDelta(ctx, "")
	}
	if errors.As(err, &ge) && ge.Status == 403 {
		return sentence(fmt.Sprintf(feedRefused, err))
	}
	if err != nil {
		return err
	}
	for _, c := range res.Users {
		if handled, err := s.agentUserChanged(ctx, c); err != nil {
			return err
		} else if handled {
			continue
		}
		// A person is written only where Entra owns people. The feed also
		// runs for agent users alone, and people then belong to another
		// source.
		if !s.readsPeople() || (c.Enabled == nil && !c.Removed) {
			continue
		}
		a := account{id: c.ID, upn: c.UPN, display: c.DisplayName, mail: c.Mail, removed: c.Removed}
		if c.Enabled != nil {
			a.enabled = *c.Enabled
		}
		if _, _, err := s.person(ctx, a, false); err != nil {
			return err
		}
	}
	return s.d.Settings.Set(ctx, usersLinkKey, res.Link)
}

// member answers the Straza user of a person in a role's group, creating
// or taking over the user as person does, or the reason the member is not
// applied. A member whose enabled state did not come back fails the read.
func (s *Syncer) member(ctx context.Context, groupID string, m entragraph.Member) (store.User, string, error) {
	if m.Enabled == nil || m.UPN == "" {
		return store.User{}, "", sentence(fmt.Sprintf(noEnabledState, groupID))
	}
	return s.person(ctx, account{id: m.ID, upn: m.UPN, display: m.DisplayName, mail: m.Mail, enabled: *m.Enabled}, true)
}
