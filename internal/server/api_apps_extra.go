package server

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/redact"
)

// oauthProviderPayload is one configured oauth provider on the admin
// surface: its name, default scopes and the redirect URI to register at the
// provider. Never the client secret.
type oauthProviderPayload struct {
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes"`
	RedirectURI string   `json:"redirect_uri"`
}

// handleOAuthProviders lists the oauth providers this server is configured
// with, sorted by name, for the credential step's provider picker.
func (a *App) handleOAuthProviders(w http.ResponseWriter, _ *http.Request) {
	out := make([]oauthProviderPayload, 0, len(a.oauthProviders))
	for name, p := range a.oauthProviders {
		out = append(out, oauthProviderPayload{Name: name, Scopes: nonNilStrings(p.Scopes), RedirectURI: a.connectRedirectURI()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

// manifestFields answers the stored manifest as every route answers a
// server's document (maskedManifest), as the JSON object of the stored
// shape, and the remote runtime's address read from that masked manifest.
// A row with no manifest, or one the mask cannot render, yields neither,
// so the list never answers a manifest unmasked.
func manifestFields(stored string) (json.RawMessage, string) {
	var mf manager.Manifest
	if err := yaml.Unmarshal([]byte(maskedManifest(stored)), &mf); err != nil || mf.Kind == "" {
		return nil, ""
	}
	masked, err := mf.JSON()
	if err != nil {
		return nil, ""
	}
	url := ""
	if mf.Straza.Runtime.Kind == manager.RuntimeRemote && mf.Straza.Runtime.Remote != nil {
		url = mf.Straza.Runtime.Remote.URL
	}
	return json.RawMessage(masked), url
}

// keptManifest answers mf, the manifest read from raw, read again with
// every mask raw sends back replaced by the value live, the stored
// manifest, holds at the same place (keptMasks), so a document read from
// the list and sent back compares with the stored manifest as the publish
// will store it. A mask that does not fit leaves mf as it is, and the
// publish refuses that mask (bundle.masked).
func keptManifest(raw []byte, mf manager.Manifest, live string) manager.Manifest {
	restored, at := keptMasks(string(raw), live)
	if restored == "" || at == "" {
		return mf
	}
	kept, err := manager.Parse([]byte(restored))
	if err != nil {
		return mf
	}
	return kept
}

// serverLogMask answers the mask the log route runs over each line of the
// server whose stored manifest is live, after the ring's own mask: every
// value the draft secret scan flags in the manifest, the longest first,
// replaced by redact.Mark wherever a line holds it, each line of such a
// value that spans lines too when it holds 8 or more characters, since a
// log line never holds the whole value, and then the scan's text mask
// (drafts.MaskText). A server that prints its own settings thus prints no
// value the scan reads as a secret. When the scan's form of the manifest
// cannot be read scalar by scalar, every value of the manifest is
// replaced, because no flagged value may stay.
func serverLogMask(live string) func(string) string {
	var values []string
	if mf, err := manager.FromJSON(live); err == nil && mf.Kind != "" {
		doc, _ := yaml.Marshal(mf)
		stamp, _ := drafts.WithoutSecrets(string(doc))
		plain, stamped := scalarsOf(parsed(string(doc)), "", "", ""), scalarsOf(parsed(stamp), "", "", "")
		for i, s := range plain {
			v := s.node.Value
			if v == "" || len(stamped) == len(plain) && stamped[i].node.Value == v {
				continue
			}
			values = append(values, v)
			for _, line := range strings.Split(v, "\n") {
				if line = strings.TrimSpace(line); strings.Contains(v, "\n") && len(line) >= 8 {
					values = append(values, line)
				}
			}
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	return func(line string) string {
		for _, v := range values {
			line = strings.ReplaceAll(line, v, redact.Mark)
		}
		return drafts.MaskText(line)
	}
}

// handleAppsImport converts an MCP registry server.json into an app.yaml
// manifest with the same code as strazactl apps import, without installing
// anything.
func (a *App) handleAppsImport(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		apiError(w, http.StatusBadRequest, "read body failed")
		return
	}
	mf, err := manager.Import(raw, manager.ImportOptions{})
	if err != nil {
		apiError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	doc, err := yaml.Marshal(mf)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "render manifest failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"manifest": string(doc), "name": mf.Metadata.Name, "runtime": mf.Straza.Runtime.Kind,
	})
}
