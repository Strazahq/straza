package scim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/strazahq/straza/internal/store"
)

// userDocument is the accepted POST/PUT wire shape (mapped subset).
type userDocument struct {
	Schemas    []string `json:"schemas"`
	ExternalID string   `json:"externalId"`
	UserName   string   `json:"userName"`
	Display    string   `json:"displayName"`
	// Title is SCIM CORE (RFC 7643 §4.1.1), adopted in revision 10: the
	// job-title/function line for humans and agents alike ("Deploy bot").
	Title string `json:"title"`
	Name  struct {
		Formatted string `json:"formatted"`
	} `json:"name"`
	Emails []struct {
		Value   string `json:"value"`
		Primary bool   `json:"primary"`
	} `json:"emails"`
	Active any `json:"active"` // bool or Azure-style "True"/"False"
	// Identity typology (spec/scim-profile §3.2, revision 5). UserType is the
	// SCIM CORE attribute with a Straza-enforced vocabulary; the writable
	// extension fields ride the same URN as the read-only lock block. On PUT,
	// absent/empty typology fields are left UNTOUCHED (documented deviation:
	// an IdM that does not map them must not clobber out-of-band
	// classification on every recon); clearing goes through PATCH remove.
	UserType  string        `json:"userType"`
	Extension *extensionDoc `json:"urn:straza:params:scim:schemas:extension:2.0:User"`
	// SecondExtension is the same extension under its second name, the form
	// Microsoft Entra ID asks of a custom attribute (spec/scim-profile §3.6).
	SecondExtension *extensionDoc `json:"urn:ietf:params:scim:schemas:extension:straza:2.0:User"`
	// Enterprise carries the manager, which becomes an agent's sponsor.
	Enterprise *enterpriseDoc `json:"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"`
}

// extensionDoc is the writable half of the Straza User extension.
type extensionDoc struct {
	AgencyMode string `json:"agencyMode"`
	Sponsor    string `json:"sponsor"`
	SwarmID    string `json:"swarmId"`
	Ephemeral  any    `json:"ephemeral"` // bool or Azure-style "True"/"False"; nil = absent
	EntraID    string `json:"entraAgentId"`
}

func (d userDocument) displayName() string {
	if d.Display != "" {
		return d.Display
	}
	return d.Name.Formatted
}

func (d userDocument) email() string {
	for _, e := range d.Emails {
		if e.Primary {
			return e.Value
		}
	}
	if len(d.Emails) > 0 {
		return d.Emails[0].Value
	}
	return ""
}

func (d userDocument) isAgent() bool {
	for _, s := range d.Schemas {
		if strings.HasPrefix(s, AgentSchemaPrefix) {
			return true
		}
	}
	return false
}

// parseActive accepts SCIM booleans and the string forms Azure AD sends.
func parseActive(v any) (bool, error) {
	switch t := v.(type) {
	case nil:
		return true, nil
	case bool:
		return t, nil
	case string:
		b, err := strconv.ParseBool(strings.ToLower(t))
		if err != nil {
			return false, errors.New("active must be a boolean (or \"True\"/\"False\")")
		}
		return b, nil
	default:
		return false, errors.New("active must be a boolean")
	}
}

// Typology vocabularies (spec/scim-profile §3.2): the store constants are
// the single source; a value outside them is invalidValue, never stored.
var (
	scimUserTypes   = map[string]bool{store.UserTypeHuman: true, store.UserTypeAgent: true, store.UserTypeService: true}
	scimAgencyModes = map[string]bool{store.AgencyInteractive: true, store.AgencySupervised: true, store.AgencyAutonomous: true}
)

// normVocab lowercases and validates an enum-typed typology value.
func normVocab(v any, attr string, allowed map[string]bool) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", attr)
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if !allowed[s] {
		keys := make([]string, 0, len(allowed))
		for k := range allowed {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return "", fmt.Errorf("%s %q is not a value Straza accepts. Use one of %v", attr, v, keys)
	}
	return s, nil
}

// parseEphemeral mirrors parseActive's leniency; nil is NOT accepted here
// because callers pass it only when the attribute is present.
func parseEphemeral(v any) (bool, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	case string:
		b, err := strconv.ParseBool(strings.ToLower(t))
		if err != nil {
			return false, errors.New("ephemeral must be a boolean (or \"True\"/\"False\")")
		}
		return b, nil
	default:
		return false, errors.New("ephemeral must be a boolean")
	}
}

// applyTypologyDoc applies the POST/PUT typology subset onto the user:
// present values set (validated), absent/empty leave the stored value
// untouched (§3.2 PUT deviation; see userDocument).
func applyTypologyDoc(u *store.User, doc userDocument) error {
	if doc.UserType != "" {
		v, err := normVocab(doc.UserType, "userType", scimUserTypes)
		if err != nil {
			return err
		}
		u.UserType = v
	}
	// The second name is applied first, so a value under Straza's own name
	// is the one stored, and both are checked.
	for _, ext := range []*extensionDoc{doc.SecondExtension, doc.Extension} {
		if err := applyExtensionDoc(u, ext); err != nil {
			return err
		}
	}
	return nil
}

// applyExtensionDoc applies one Straza extension block of a POST or PUT
// document onto the user, as applyTypologyDoc says. A nil block applies
// nothing.
func applyExtensionDoc(u *store.User, ext *extensionDoc) error {
	if ext == nil {
		return nil
	}
	if ext.AgencyMode != "" {
		v, err := normVocab(ext.AgencyMode, "agencyMode", scimAgencyModes)
		if err != nil {
			return err
		}
		u.AgencyMode = v
	}
	if ext.Sponsor != "" {
		u.Sponsor = ext.Sponsor
	}
	if ext.SwarmID != "" {
		u.SwarmID = ext.SwarmID
	}
	if ext.EntraID != "" {
		id, err := normEntraAgentID(ext.EntraID)
		if err != nil {
			return err
		}
		u.EntraAgentID = id
	}
	if ext.Ephemeral != nil {
		b, err := parseEphemeral(ext.Ephemeral)
		if err != nil {
			return err
		}
		u.Ephemeral = b
	}
	return nil
}

// userKind derives the §3.3 `kind` fact from the create-time attrs mark:
// `nhi` iff the create carried the agentic extension URN, `human` otherwise
// (including unparseable or out-of-vocabulary attrs: fail toward human, the
// value that grants nothing NHI-specific).
func userKind(u store.User) string {
	var attrs map[string]any
	if u.Attrs != "" {
		_ = json.Unmarshal([]byte(u.Attrs), &attrs)
	}
	if attrs["kind"] == "nhi" {
		return "nhi"
	}
	return "human"
}

// userResource renders one user in SCIM shape.
func userResource(u store.User) map[string]any {
	schemas := []string{SchemaUser}
	if userKind(u) == "nhi" {
		schemas = append(schemas, AgentSchemaPrefix+":2.0:Agent")
	}
	res := map[string]any{
		"schemas":  schemas,
		"id":       u.ID,
		"userName": u.Username,
		"active":   u.Status == store.UserActive,
		"meta": map[string]any{
			"resourceType": "User",
			"created":      u.CreatedAt.UTC(),
			"lastModified": u.UpdatedAt.UTC(),
			"location":     "/scim/v2/Users/" + u.ID,
		},
	}
	if u.ExternalID != "" {
		res["externalId"] = u.ExternalID
	}
	if u.Display != "" {
		res["displayName"] = u.Display
	}
	if u.Title != "" {
		res["title"] = u.Title
	}
	if u.Email != "" {
		res["emails"] = []map[string]any{{"value": u.Email, "primary": true}}
	}
	if u.UserType != "" {
		res["userType"] = u.UserType
	}
	return res
}

// extensionBlock renders the Straza extension: the read-only lock lane
// (§3.1), the writable typology (§3.2), and the Straza-born facts (§3.3).
// Only admin/external rows count as a LOCK: a scim-origin row is just the
// mirror of active:false, which the IdM already sees on `active`. `locked`,
// `kind` and `origin` are always present (so IdM inbound mappings have
// stable attributes); everything else rides only when set; the newest lock
// wins when several exist.
func extensionBlock(u store.User, rows []store.Revocation) map[string]any {
	block := map[string]any{"locked": false, "kind": userKind(u), "origin": u.Origin}
	for _, rv := range rows {
		if rv.Origin == store.RevocationOriginSCIM {
			continue
		}
		block["locked"] = true
		block["lockReason"] = rv.Reason
		block["lockedAt"] = rv.CreatedAt.UTC()
		block["lockOrigin"] = rv.Origin
	}
	if u.AgencyMode != "" {
		block["agencyMode"] = u.AgencyMode
	}
	if u.Sponsor != "" {
		block["sponsor"] = u.Sponsor
	}
	if u.SwarmID != "" {
		block["swarmId"] = u.SwarmID
	}
	if u.EntraAgentID != "" {
		block["entraAgentId"] = u.EntraAgentID
	}
	if u.Ephemeral {
		block["ephemeral"] = true
	}
	return block
}

// withExtension attaches the extension block to a rendered User.
func withExtension(res map[string]any, u store.User, rows []store.Revocation) map[string]any {
	res["schemas"] = append(res["schemas"].([]string), SchemaStrazaUser)
	res[SchemaStrazaUser] = extensionBlock(u, rows)
	return res
}

// userResourceCtx renders a User INCLUDING the read-only `groups` attribute
// (RFC 7643 §4.1.2) and the Straza lock extension (§3.1). Since revision
// 12 `groups` reflects the user's DIRECT assignments of exported roles
// (value = role id, display = role name): the wire-groups ARE roles, and
// connectors diff desired-vs-current membership against this. Membership
// writes still go through Groups PATCH only; `groups` stays outside the
// accepted PATCH subset.
func (s *Server) userResourceCtx(ctx context.Context, u store.User) map[string]any {
	res := userResource(u)
	if rows, err := s.deps.Store.Revocations().ListByTarget(ctx, store.RevokeUser, u.ID); err == nil {
		res = withExtension(res, u, rows)
	}
	asg, err := s.deps.Store.Roles().ListAssignments(ctx, store.SubjectUser, u.ID)
	if err != nil || len(asg) == 0 {
		return res
	}
	groups := make([]map[string]any, 0, len(asg))
	for _, a := range asg {
		role, err := s.deps.Store.Roles().GetByID(ctx, a.RoleID)
		if err != nil {
			continue
		}
		groups = append(groups, map[string]any{
			"value": role.ID, "display": role.Name,
			"type": "direct", "$ref": "/scim/v2/Groups/" + role.ID,
		})
	}
	if len(groups) > 0 {
		res["groups"] = groups
	}
	return res
}

func (s *Server) handleUsersList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if raw := r.URL.Query().Get("filter"); raw != "" {
		attr, value, err := parseFilter(raw, "userName", "externalId")
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalidFilter", err.Error())
			return
		}
		var u store.User
		if attr == "username" {
			u, err = s.deps.Store.Users().GetByUsername(ctx, value)
		} else {
			u, err = s.deps.Store.Users().GetByExternalID(ctx, value)
		}
		if errors.Is(err, store.ErrNotFound) ||
			(err == nil && s.deps.ProtectedUsername != "" && u.Username == s.deps.ProtectedUsername) {
			listResponse(w, nil, 0, 1)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "", "lookup failed")
			return
		}
		listResponse(w, []map[string]any{s.userResourceCtx(ctx, u)}, 1, 1)
		return
	}

	all, err := s.deps.Store.Users().List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "list failed")
		return
	}
	// The break-glass account never renders (revision 12).
	users := make([]store.User, 0, len(all))
	for _, u := range all {
		if s.deps.ProtectedUsername != "" && u.Username == s.deps.ProtectedUsername {
			continue
		}
		users = append(users, u)
	}
	// One revocations read serves the whole page: an IdM full sync pages
	// through here, and recon must see the lock lane (§3.1) without N+1
	// per-user queries.
	byUser := map[string][]store.Revocation{}
	if revs, err := s.deps.Store.Revocations().List(ctx); err == nil {
		for _, rv := range revs {
			if rv.Kind == store.RevokeUser {
				byUser[rv.TargetID] = append(byUser[rv.TargetID], rv)
			}
		}
	}
	listPage(w, r, users, func(u store.User) map[string]any {
		return withExtension(userResource(u), u, byUser[u.ID])
	})
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var doc userDocument
	if err := json.NewDecoder(r.Body).Decode(&doc); err != nil {
		if detail := wrongTypeDetail(err); detail != "" {
			// The decoder reads on past a value of the wrong type, so the
			// refusal can still name the userName the body asks for.
			noteUser(w, doc.UserName)
			writeError(w, http.StatusBadRequest, "invalidValue", detail)
			return
		}
		writeError(w, http.StatusBadRequest, "invalidSyntax", "malformed JSON body")
		return
	}
	noteUser(w, doc.UserName)
	if doc.UserName == "" {
		writeError(w, http.StatusBadRequest, "invalidValue", "userName is required")
		return
	}
	active, err := parseActive(doc.Active)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}

	u := store.User{
		ExternalID: doc.ExternalID,
		Username:   doc.UserName,
		Email:      doc.email(),
		Display:    doc.displayName(),
		Title:      doc.Title,
		Origin:     store.OriginSCIM,
		Status:     store.UserActive,
	}
	if !active {
		u.Status = store.UserDisabled
	}
	if doc.isAgent() {
		// Agentic extension: a create that names no userType is typed agent
		// here, and a named one replaces it below, so no non-human row is born
		// untyped.
		u.Attrs = `{"kind":"nhi"}`
		u.UserType = store.UserTypeAgent
	}
	if err := applyTypologyDoc(&u, doc); err != nil {
		writeError(w, http.StatusBadRequest, "invalidValue", err.Error())
		return
	}
	if !s.applyManager(w, r, &u, doc.managerWrite()) || s.refuseEntraAgentID(w, r, store.User{}, u) {
		return
	}
	created, err := s.deps.Store.Users().Create(r.Context(), u)
	if errors.Is(err, store.ErrConflict) {
		if revived, ok := s.reviveDeactivated(r.Context(), u); ok {
			s.deps.IdentityChanged(r.Context(), "straza.identity.created", revived.ID)
			s.userCreated(r.Context(), revived)
			w.Header().Set("Location", "/scim/v2/Users/"+revived.ID)
			writeSCIM(w, http.StatusCreated, s.userResourceCtx(r.Context(), revived))
			return
		}
		if u.EntraAgentID != "" && s.refuseEntraAgentID(w, r, store.User{}, u) {
			return
		}
		writeError(w, http.StatusConflict, "uniqueness", "userName or externalId already exists")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "create failed")
		return
	}
	s.deps.IdentityChanged(r.Context(), "straza.identity.created", created.ID)
	s.userCreated(r.Context(), created)
	w.Header().Set("Location", "/scim/v2/Users/"+created.ID)
	writeSCIM(w, http.StatusCreated, s.userResourceCtx(r.Context(), created))
}

func (s *Server) userByID(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	u, err := s.deps.Store.Users().GetByID(r.Context(), r.PathValue("id"))
	// The break-glass account does not exist on this wire (revision 12):
	// no IdM op may see, edit, deactivate, or revive it.
	if errors.Is(err, store.ErrNotFound) || (err == nil && s.deps.ProtectedUsername != "" && u.Username == s.deps.ProtectedUsername) {
		writeError(w, http.StatusNotFound, "", "no such user")
		return store.User{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "lookup failed")
		return store.User{}, false
	}
	noteUser(w, u.Username)
	return u, true
}

func (s *Server) handleUserGet(w http.ResponseWriter, r *http.Request) {
	if u, ok := s.userByID(w, r); ok {
		writeSCIM(w, http.StatusOK, s.userResourceCtx(r.Context(), u))
	}
}

// strazaURNLower is the extension URN as normalizePath renders it.
const strazaURNLower = "urn:straza:params:scim:schemas:extension:2.0:user"

// strazaSecondURNLower is the second name of the extension (§3.6) as
// normalizePath renders it.
const strazaSecondURNLower = "urn:ietf:params:scim:schemas:extension:straza:2.0:user"

// strazaWritableAttrs are the writable attributes of the extension, as
// normalizePath renders them.
var strazaWritableAttrs = []string{"agencymode", "sponsor", "swarmid", "ephemeral", "entraagentid"}

// coreUserURNLower is the core User schema URN as normalizePath renders it.
const coreUserURNLower = "urn:ietf:params:scim:schemas:core:2.0:user"

// lockAttrPath reports whether a normalized (lowercased) PATCH path targets
// the read-only lock half of the Straza extension (§3.1): the bare
// attribute names or the URN-qualified forms IdMs send.
func lockAttrPath(p string) bool {
	p = strings.TrimPrefix(p, strazaURNLower+":")
	switch p {
	case "locked", "lockreason", "lockedat", "lockorigin":
		return true
	}
	return false
}

// strazaBornFactPath reports whether a normalized (lowercased) PATCH path
// targets a §3.3 Straza-born fact (`kind`/`origin`), bare or URN-qualified.
func strazaBornFactPath(p string) bool {
	p = strings.TrimPrefix(p, strazaURNLower+":")
	return p == "kind" || p == "origin"
}

// userPatch is what a PATCH's operations ask for beside the user's own
// fields: the active state, and the manager and sponsor applyManager judges
// once every operation is applied.
type userPatch struct {
	active  bool
	manager managerWrite
	// own lists the attributes the PATCH has named outside the second name
	// of the extension, bare or under Straza's own name, which an operation
	// under the second name does not replace.
	own []string
}

// applyUserOp mutates the mapped attributes for one PATCH operation. An
// attribute Straza does not map is ignored, as a create ignores it, so an
// identity manager that maps more than Straza stores is not refused.
func applyUserOp(u *store.User, w *userPatch, op patchOp) error {
	// A path may name a core attribute with its schema URN (RFC 7644 §3.10),
	// or be that URN alone with an object value. It is judged as the bare
	// path or as a PATCH without a path, so a mapped attribute, a
	// deactivation above all, is never ignored for the form of its path.
	op.path = strings.TrimPrefix(op.path, coreUserURNLower+":")
	if op.path == coreUserURNLower {
		op.path = ""
	}
	// The extension under its second name is judged as under its own, so the
	// read-only half below is refused under both.
	if op.path == strazaSecondURNLower || strings.HasPrefix(op.path, strazaSecondURNLower+":") {
		op.path, op.second = strazaURNLower+op.path[len(strazaSecondURNLower):], true
	}
	if lockAttrPath(op.path) {
		return mutabilityError{"the Straza lock block is read-only over SCIM, because a lock is Straza's own decision about the account. Lift it through the Straza admin API"}
	}
	if strazaBornFactPath(op.path) {
		return mutabilityError{"kind and origin are Straza-born facts, read-only over SCIM: kind is fixed at create by the agentic extension and origin records how the account was born. Leave both out of the PATCH"}
	}
	if op.path == "groups" || strings.HasPrefix(op.path, "groups[") || strings.HasPrefix(op.path, "groups.") {
		return mutabilityError{"a user's groups are read-only over SCIM, because a membership is written on the group. Send the membership as a members operation on /scim/v2/Groups/{id}"}
	}
	if w.manager.patch(op) {
		return nil
	}
	// Whole-extension PATCH: apply per key, so writable typology passes and
	// any lock key inside still refuses (§3.1/§3.2).
	if op.path == strazaURNLower {
		if op.op == "remove" {
			for _, attr := range strazaWritableAttrs {
				sub := patchOp{op: op.op, path: strazaURNLower + ":" + attr, second: op.second}
				if err := applyUserOp(u, w, sub); err != nil {
					return err
				}
			}
			return nil
		}
		obj, ok := op.value.(map[string]any)
		if !ok {
			return errors.New("extension PATCH value must be an object")
		}
		for k, v := range obj {
			sub := patchOp{op: op.op, path: strazaURNLower + ":" + strings.ToLower(k), value: v, second: op.second}
			if err := applyUserOp(u, w, sub); err != nil {
				return err
			}
		}
		return nil
	}
	// An attribute a PATCH names under both names of the extension keeps the
	// value under Straza's own name, bare or qualified, in either order. The
	// form of the value under the second name is still checked, on a copy
	// that is never stored.
	attr := strings.TrimPrefix(op.path, strazaURNLower+":")
	switch {
	case !op.second:
		w.own = append(w.own, attr)
	case slices.Contains(w.own, attr):
		checked := *u
		u = &checked
	}
	switch op.path {
	case "usertype":
		if op.op == "remove" {
			u.UserType = ""
			return nil
		}
		v, err := normVocab(op.value, "userType", scimUserTypes)
		if err != nil {
			return err
		}
		u.UserType = v
		return nil
	case "agencymode", strazaURNLower + ":agencymode":
		if op.op == "remove" {
			u.AgencyMode = ""
			return nil
		}
		v, err := normVocab(op.value, "agencyMode", scimAgencyModes)
		if err != nil {
			return err
		}
		u.AgencyMode = v
		return nil
	case "sponsor", strazaURNLower + ":sponsor":
		w.manager.sponsor = true
		if op.op == "remove" {
			u.Sponsor = ""
			return nil
		}
		v, _ := op.value.(string)
		u.Sponsor = v
		return nil
	case "entraagentid", strazaURNLower + ":entraagentid":
		return applyEntraOp(u, op)
	case "swarmid", strazaURNLower + ":swarmid":
		if op.op == "remove" {
			u.SwarmID = ""
			return nil
		}
		v, _ := op.value.(string)
		u.SwarmID = v
		return nil
	case "ephemeral", strazaURNLower + ":ephemeral":
		if op.op == "remove" {
			u.Ephemeral = false
			return nil
		}
		b, err := parseEphemeral(op.value)
		if err != nil {
			return err
		}
		u.Ephemeral = b
		return nil
	}
	switch op.path {
	case "":
		if op.op != "replace" && op.op != "add" {
			return errors.New("no-path PATCH supports add/replace with an object value")
		}
		obj, ok := op.value.(map[string]any)
		if !ok {
			return errors.New("no-path PATCH value must be an object")
		}
		for k, v := range obj {
			sub := patchOp{op: op.op, path: strings.ToLower(k), value: v}
			if err := applyUserOp(u, w, sub); err != nil {
				return err
			}
		}
		return nil
	case "active":
		if op.op == "remove" {
			return mutabilityError{"active cannot be removed"}
		}
		b, err := parseActive(op.value)
		if err != nil {
			return err
		}
		w.active = b
		return nil
	case "username":
		if op.op == "remove" {
			return mutabilityError{"userName is required and cannot be removed"}
		}
		v, ok := op.value.(string)
		if !ok || v == "" {
			return errors.New("userName must be a non-empty string")
		}
		u.Username = v
		return nil
	case "name":
		// The name object: formatted is its one mapped sub-attribute.
		obj, _ := op.value.(map[string]any)
		for k, v := range obj {
			if strings.EqualFold(k, "formatted") {
				return applyUserOp(u, w, patchOp{op: op.op, path: "name.formatted", value: v})
			}
		}
		return nil
	case "displayname", "name.formatted":
		if op.op == "remove" {
			u.Display = ""
			return nil
		}
		v, _ := op.value.(string)
		u.Display = v
		return nil
	case "title":
		if op.op == "remove" {
			u.Title = ""
			return nil
		}
		v, _ := op.value.(string)
		u.Title = v
		return nil
	case "externalid":
		if op.op == "remove" {
			u.ExternalID = ""
			return nil
		}
		v, _ := op.value.(string)
		u.ExternalID = v
		return nil
	case "emails":
		if op.op == "remove" {
			u.Email = ""
			return nil
		}
		raw, err := json.Marshal(op.value)
		if err != nil {
			return errors.New("emails value is not JSON")
		}
		var doc userDocument
		if err := json.Unmarshal([]byte(`{"emails":`+string(raw)+`}`), &doc); err != nil {
			return errors.New("emails must be a list of {value, primary}")
		}
		u.Email = doc.email()
		return nil
	}
	// Straza stores one address, so the value of any one filtered address,
	// the form Entra sends, is that address.
	if emailValuePathRe.MatchString(op.path) {
		v, _ := op.value.(string)
		u.Email = v
	}
	return nil
}
