package scim

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/strazahq/straza/internal/store"
)

// enterpriseURNLower is the URN of the SCIM enterprise User extension as
// normalizePath renders it.
const enterpriseURNLower = "urn:ietf:params:scim:schemas:extension:enterprise:2.0:user"

// The SCIM error details of an agent's manager Straza refuses.
const (
	managerShapeMsg = "manager must be a string or an object with a value, holding the manager's Straza id or userName. " +
		"Send it in one of those two forms"
	managerUnknownMsg = "manager %q is not a user Straza knows, so it cannot become the sponsor of the agent %s. " +
		"Provision the manager first, or send the manager's Straza id or userName, then send the agent again"
	managerCheckFailedMsg = "Straza could not look up the manager, because its database did not answer, so nothing was saved. " +
		"Send the user again. The identity manager's next reconciliation sends it again too"
)

// enterpriseDoc is the one attribute Straza reads from the SCIM enterprise
// User extension on a POST or a PUT.
type enterpriseDoc struct {
	Manager any `json:"manager"` // a string, or the RFC 7643 object with a value
}

// managerWrite is what one SCIM write says about an agent's sponsor: the
// enterprise manager it names, and whether it names the Straza sponsor
// attribute itself, which then wins. Nothing in it is judged until
// applyManager knows the user is an agent, so a manager never refuses a
// write of any other user.
type managerWrite struct {
	named    bool   // the write names the manager
	value    string // the manager's Straza id or userName, empty to clear the sponsor
	unread   bool   // the manager came in a shape Straza does not read
	sponsor  bool   // the write names the Straza sponsor attribute
	document bool   // the manager came in a POST or PUT document, which never fails on it
}

// managerValue answers the manager a SCIM value names, and false when the
// value has a shape Straza does not read. A plain string, as Entra sends it,
// and the value of the RFC 7643 manager object name one. A null, an empty
// object and an object with a null value name none.
func managerValue(v any) (string, bool) {
	obj, isObj := v.(map[string]any)
	if isObj {
		if len(obj) == 0 {
			return "", true
		}
		val, has := obj["value"]
		for k, x := range obj {
			if !has && strings.EqualFold(k, "value") {
				val, has = x, true
			}
		}
		if !has {
			return "", false
		}
		v = val
	}
	switch t := v.(type) {
	case nil:
		return "", true
	case string:
		return strings.TrimSpace(t), true
	}
	return "", false
}

// set records the manager a PATCH add or replace names, or its remove.
func (m *managerWrite) set(op string, v any) {
	m.named, m.value, m.unread = true, "", false
	if op != "remove" {
		value, ok := managerValue(v)
		m.value, m.unread = value, !ok
	}
}

// patch records op when its path names the enterprise manager, bare,
// URN-qualified or inside the whole enterprise extension, and answers
// whether the path was the manager's or that extension's. Every other
// attribute of the extension is ignored.
func (m *managerWrite) patch(op patchOp) bool {
	switch op.path {
	case "manager", "manager.value", enterpriseURNLower + ":manager", enterpriseURNLower + ":manager.value":
		m.set(op.op, op.value)
	case enterpriseURNLower:
		if op.op == "remove" {
			m.set(op.op, nil)
			break
		}
		obj, _ := op.value.(map[string]any)
		for k, v := range obj {
			if strings.EqualFold(k, "manager") {
				m.set(op.op, v)
			}
		}
	default:
		return false
	}
	return true
}

// managerWrite answers what a POST or PUT document says about the manager
// and the sponsor. An absent or empty manager names none, so the stored
// sponsor stays, as an absent typology attribute does.
func (d userDocument) managerWrite() managerWrite {
	m := managerWrite{document: true}
	for _, ext := range []*extensionDoc{d.Extension, d.SecondExtension} {
		m.sponsor = m.sponsor || ext != nil && ext.Sponsor != ""
	}
	if d.Enterprise == nil {
		return m
	}
	value, ok := managerValue(d.Enterprise.Manager)
	m.named, m.value, m.unread = value != "" || !ok, value, !ok
	return m
}

// applyManager makes the manager a write named the sponsor of u, when u is
// an agent after the write and the write named no sponsor itself. It looks
// the manager up as a Straza id, then as a userName, and stores the
// userName. An empty manager clears the sponsor. A manager in a shape Straza
// does not read and a manager Straza does not know go to skipManager, and
// the break-glass account answers as an unknown manager. It writes a 500 and
// answers false when the lookup failed, so nothing is saved on a manager it
// could not check.
func (s *Server) applyManager(w http.ResponseWriter, r *http.Request, u *store.User, m managerWrite) bool {
	if !m.named || m.sponsor || u.UserType != store.UserTypeAgent {
		return true
	}
	if m.unread {
		return s.skipManager(w, u, m, managerShapeMsg)
	}
	if m.value == "" {
		u.Sponsor = ""
		return true
	}
	users := s.deps.Store.Users()
	mgr, err := users.GetByID(r.Context(), m.value)
	if errors.Is(err, store.ErrNotFound) {
		mgr, err = users.GetByUsername(r.Context(), m.value)
	}
	switch {
	case errors.Is(err, store.ErrNotFound),
		err == nil && s.deps.ProtectedUsername != "" && mgr.Username == s.deps.ProtectedUsername:
		return s.skipManager(w, u, m, fmt.Sprintf(managerUnknownMsg, m.value, u.Username))
	case err != nil:
		writeError(w, http.StatusInternalServerError, "", managerCheckFailedMsg)
		return false
	}
	u.Sponsor = mgr.Username
	return true
}

// skipManager answers for a manager Straza cannot make the sponsor of u,
// with detail as the reason. A PATCH is refused with a 400 and false. A POST
// or PUT document was always accepted with a manager Straza did not read, so
// it logs one warning and answers true, and u is saved with the sponsor it
// had.
func (s *Server) skipManager(w http.ResponseWriter, u *store.User, m managerWrite, detail string) bool {
	if m.document {
		s.deps.Log.Warn("SCIM manager not applied", "user", u.Username, "reason", detail)
		return true
	}
	writeError(w, http.StatusBadRequest, "invalidValue", detail)
	return false
}
