package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// guidForm is the textual form of a GUID that Entra shows for a client id.
var guidForm = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// EntraTenant returns the tenant that an Entra token endpoint names, a
// tenant id or a domain. It refuses a tokenURL that is not the v2.0 token
// endpoint of one tenant, because the client credentials grant and the agent
// identity exchange need one tenant and the v2.0 form. The error is a
// sentence about entraBlueprint, the one setting that needs the tenant.
func EntraTenant(tokenURL string) (string, error) {
	u, err := url.Parse(tokenURL)
	if err != nil {
		return "", errors.New("entraBlueprint needs a tokenUrl that parses as a URL. Write the token endpoint of your tenant, https://login.microsoftonline.com/<tenant id>/oauth2/v2.0/token")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] != "oauth2" || parts[2] != "v2.0" || parts[3] != "token" {
		return "", fmt.Errorf("entraBlueprint needs tokenUrl in the form https://login.microsoftonline.com/<tenant id>/oauth2/v2.0/token, the v2.0 token endpoint of one tenant, and its path is %q. Write the token endpoint of your tenant", u.Path)
	}
	switch strings.ToLower(parts[0]) {
	case "common", "organizations", "consumers":
		return "", fmt.Errorf("entraBlueprint needs a tokenUrl with one tenant, and this one names %s. Write the token endpoint of your tenant, https://login.microsoftonline.com/<tenant id>/oauth2/v2.0/token", parts[0])
	}
	return parts[0], nil
}

// validateEntra checks a block that names an Entra agent identity blueprint:
// a GUID, no assertionAudience, a tokenUrl of one tenant and exactly one
// scope ending in /.default, the only scope form Entra's client credentials
// grant takes.
func (c *ClientCredentials) validateEntra(field, tokenURL string) error {
	if c.AssertionAudience != "" {
		return fmt.Errorf("%s: entraBlueprint and assertionAudience exclude each other, because Entra takes the token endpoint as the assertion's audience. Remove assertionAudience", field)
	}
	if !guidForm.MatchString(c.EntraBlueprint) {
		return fmt.Errorf("%s: entraBlueprint %q is not a GUID. Copy the application (client) id of the agent blueprint from the Entra admin centre", field, c.EntraBlueprint)
	}
	if _, err := EntraTenant(tokenURL); err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if len(c.Scopes) != 1 || !strings.HasSuffix(c.Scopes[0], "/.default") {
		return fmt.Errorf("%s: entraBlueprint needs exactly one scope that ends in /.default, such as api://<resource app id>/.default, because Entra's client credentials grant takes only that form. Write the resource's /.default scope under scopes", field)
	}
	return nil
}
