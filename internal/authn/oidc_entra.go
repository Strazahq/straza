package authn

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// entraTenant is the form of a tenant id in an Entra issuer: a GUID in
// lowercase, as Entra writes it.
var entraTenant = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// entraIssuer reports whether issuer is the v2.0 issuer of one Microsoft
// Entra tenant on the public cloud, https://login.microsoftonline.com/<tenant
// id>/v2.0. Only such an issuer's tokens have their oid claim read, because
// Microsoft defines oid as the user's object id, the same for every app of
// the tenant, while another provider's claim of that name may carry a value
// its user chose.
func entraIssuer(issuer string) bool {
	rest, ok := strings.CutPrefix(issuer, "https://login.microsoftonline.com/")
	if !ok {
		return false
	}
	tenant, ok := strings.CutSuffix(rest, "/v2.0")
	return ok && entraTenant.MatchString(tenant)
}

// entraOID reads the oid claim of an Entra ID token by its exact name, and
// answers "" when the token has none or its value is not a string. A struct
// field would also take a claim named OID or Oid, because encoding/json folds
// case, and would fail the whole token on a value of another type.
func entraOID(idToken *oidc.IDToken) string {
	var raw map[string]json.RawMessage
	if err := idToken.Claims(&raw); err != nil {
		return ""
	}
	var oid string
	if err := json.Unmarshal(raw["oid"], &oid); err != nil {
		return ""
	}
	return oid
}
