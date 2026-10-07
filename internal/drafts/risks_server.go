package drafts

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/strazahq/straza/internal/redact"
)

// serverRisks answers the risks of the servers the draft puts or removes.
func (c *check) serverRisks() []Finding {
	var out []Finding
	for _, it := range c.d.Items {
		live, had := c.w.Apps[it.Name]
		next, put := c.after.Apps[it.Name]
		object := it.Object()
		switch {
		case it.Kind != KindApp:
		case it.Op == OpRemove && had:
			out = append(out, risk(codeRemoval, object, it.Name, fmt.Sprintf(
				"Publishing removes %s: its access rows, its stored secrets and every person's connection go, with the roles it owns and its admin role.", it.Name),
				string(c.w.Fingerprints[object]), "removed"))
		case it.Op == OpPut && put:
			out = append(out, runsCode(it.Name, live, had, next)...)
			out = append(out, c.hostRisks(it.Name, live, had, next)...)
			out = append(out, agentsRisk(it.Name, live, had, next)...)
			if had {
				out = append(out, sharedRisks(it.Name, live, next)...)
				out = append(out, scopesRisk(it.Name, live, next)...)
			}
		}
	}
	return out
}

// runsCode answers server.runs-code for a command or container server the
// draft adds, or whose runtime it changes, an env value or an argument
// included. The env values and the arguments are compared here, in memory,
// and the words name an entry whose value changed as "{name} (value
// changed)" and count the arguments, so neither the words, the sentence nor
// key over them carry a value, an argument or a digest of one. A
// publish acknowledges the revision the publisher read, so the key
// needs no value.
func runsCode(name string, live App, had bool, next App) []Finding {
	if next.Runtime != runtimeCommand && next.Runtime != runtimeOCI {
		return nil
	}
	before, after := "", runtimeWords(next, nil, false)
	if had {
		before, after = runtimeWords(live, nil, false), runtimeWords(next, changedEnv(live, next), !slices.Equal(live.Args, next.Args))
		if before == after {
			return nil
		}
	}
	sentence := fmt.Sprintf("Publishing starts %s on the Straza host as Straza's own user, now and after every restart.", next.Exec)
	if next.Runtime == runtimeOCI {
		sandbox := ""
		if next.Sandbox == "none" {
			sandbox = ", with no sandbox"
		}
		sentence = fmt.Sprintf("Publishing runs the container image %s on the Straza host%s, now and after every restart.", next.Image, sandbox)
	}
	return []Finding{risk(codeRunsCode, "App/"+name, name, sentence, before, after)}
}

// runtimeWords spells what a server runs: `command {exec} {n} args`, `oci
// {image} sandbox {sandbox}`, each with the names of its env entries and a
// command's workdir, or `remote {url}` with its user information and query
// values masked. An argument's text never shows, because a manifest stored
// before drafts may keep a secret there: argsChanged marks the count
// "(args changed)". An entry that changed names reads "{name} (value
// changed)", because a value such as PATH or NODE_OPTIONS changes the code
// that runs.
func runtimeWords(a App, changed map[string]bool, argsChanged bool) string {
	env := ""
	if values, _ := envOf(a); len(values) > 0 {
		names := make([]string, 0, len(values))
		for n := range values {
			if changed[n] {
				n += " (value changed)"
			}
			names = append(names, n)
		}
		sort.Strings(names)
		env = " env " + strings.Join(names, ",")
	}
	switch a.Runtime {
	case runtimeCommand:
		w := fmt.Sprintf("command %s %d args", a.Exec, len(a.Args))
		if len(a.Args) == 1 {
			w = "command " + a.Exec + " 1 arg"
		}
		if argsChanged {
			w += " (args changed)"
		}
		w += env
		if a.Workdir != "" {
			w += " workdir " + a.Workdir
		}
		return w
	case runtimeOCI:
		return "oci " + a.Image + " sandbox " + cmp.Or(a.Sandbox, "default") + env
	case runtimeRemote:
		return "remote " + maskURL(a.URL)
	}
	return ""
}

// changedEnv answers the names of the env entries that live and next both
// hold and whose values differ. A manifest that does not read counts as
// changed, as runtimeChanged reads it.
func changedEnv(live, next App) map[string]bool {
	was, okWas := envOf(live)
	is, okIs := envOf(next)
	out := map[string]bool{}
	for n, v := range is {
		if old, ok := was[n]; ok && (!okWas || !okIs || old != v) {
			out[n] = true
		}
	}
	return out
}

// envOf answers the env of a's manifest, each name with its values in
// order, or a's env names with no values and false when the manifest does
// not read.
func envOf(a App) (map[string]string, bool) {
	var m struct {
		Straza struct {
			Runtime struct {
				Command, OCI struct {
					Env []struct{ Name, Value string }
				}
			}
		}
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(a.Manifest), &m); err != nil || a.Manifest == "" {
		for _, n := range a.EnvNames {
			out[n] = ""
		}
		return out, false
	}
	for _, e := range append(m.Straza.Runtime.Command.Env, m.Straza.Runtime.OCI.Env...) {
		out[e.Name] += e.Value + "\x00"
	}
	return out, true
}

// hostRisks answers the risks of a remote address the draft sets: a
// credential sent to a host the server has not used, calls sent over plain
// http where the server used https, a host of a class strazad should not
// reach, and a host no server uses or a path moved on the same host.
func (c *check) hostRisks(name string, live App, had bool, next App) []Finding {
	wasRemote := had && live.Runtime == runtimeRemote
	if next.Runtime != runtimeRemote || (wasRemote && live.URL == next.URL) {
		return nil
	}
	object, host, oldHost, oldAddr := "App/"+name, normHost(next.URL), "", ""
	if wasRemote {
		oldHost, oldAddr = normHost(live.URL), normHost(live.URL)+pathWords(live.URL)
	}
	var out []Finding
	if kind := credentialSent(next); host != oldHost && kind != "" {
		out = append(out, risk(codeCredentialHost, object, host, fmt.Sprintf("Straza will send %s to %s, a host %s has not used before.", credentialWords(name, kind), host, name),
			oldHost, kind+" "+host))
	}
	if wasRemote && host == oldHost && schemeOf(live.URL) == "https" && schemeOf(next.URL) == "http" {
		what := "every call travels"
		if kind := credentialSent(next); kind != "" {
			what = credentialWords(name, kind) + " and every call travel"
		}
		out = append(out, risk(codePlainHTTP, object, host, fmt.Sprintf("%s will be reached at %s over plain http, where it used https, so %s unencrypted.", name, host, what),
			"https://"+host+pathWords(live.URL), "http://"+host+pathWords(next.URL)))
	}
	if phrase, class := unusualHost(host); host != oldHost && class != "" {
		out = append(out, risk(codeUnusualHost, object, host, fmt.Sprintf("%s is reached at %s, %s, where Straza's own requests reach what they should not.%s", name, host, phrase, dialNote(class)), "", host+" "+class))
	}
	switch {
	case host != oldHost && !c.hostInUse(host):
		out = append(out, risk(codeNewHost, object, "", fmt.Sprintf("%s will be reached at %s, a host no server uses today.", name, host), oldAddr, host+pathWords(next.URL)))
	case host == oldHost && pathWords(live.URL) != pathWords(next.URL):
		out = append(out, risk(codeNewHost, object, "", fmt.Sprintf("The address of %s moves to %s on the same host.", name, pathWords(next.URL)), oldAddr, host+pathWords(next.URL)))
	}
	return out
}

// hostInUse reports whether a live remote server is reached at host.
func (c *check) hostInUse(host string) bool {
	for _, app := range c.w.Apps {
		if app.Runtime == runtimeRemote && normHost(app.URL) == host {
			return true
		}
	}
	return false
}

// The runtime and credential words of a manifest that the risks read
// beside the kinds world.go names.
const (
	runtimeCommand   = "command"
	runtimeOCI       = "oci"
	runtimeRemote    = "remote"
	credentialStatic = "static"
	authPassthrough  = "passthrough"
)

// credentialSent names the credential calls to app carry: passthrough for
// the caller's identity through auth passthrough, else the credential kind
// when it is static, token or oauth, and "" when they carry none.
func credentialSent(app App) string {
	switch {
	case app.Auth == authPassthrough:
		return authPassthrough
	case app.Credential == credentialStatic, app.Credential == CredentialToken, app.Credential == CredentialOAuth:
		return app.Credential
	}
	return ""
}

// credentialWords names what calls to the server name send, for the
// credential kind as credentialSent names it.
func credentialWords(name, kind string) string {
	switch kind {
	case authPassthrough, CredentialOAuth:
		return "every caller's sign-in"
	case CredentialToken:
		return "every caller's own token"
	}
	return name + "'s secret"
}

// normHost answers the host of the address raw in the form strazad dials,
// lower case with every label in its ASCII form.
func normHost(raw string) string {
	h, _ := asciiHost(hostOf(raw))
	return h
}

// schemeOf answers the scheme of the address raw in lower case, or "" when
// it does not parse.
func schemeOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme)
}

// pathWords spells the part of the address raw after its host: the port
// when it names one, and the path, "/" for none, masked by maskPath. The
// query is left out, because it may carry a secret.
func pathWords(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := maskPath(cmp.Or(u.EscapedPath(), "/"))
	if port := u.Port(); port != "" {
		p = ":" + port + p
	}
	return p
}

// maskPath answers the path p with each segment that the secret.path rule
// reads as a capability replaced by redact.Mark. A manifest stored before
// the secret scan may keep one in its address, and the words of a risk
// travel to every reader and into an agent's stored verdict.
func maskPath(p string) string {
	segments := strings.Split(p, "/")
	for i, seg := range segments {
		if secretCapability(seg) {
			segments[i] = redact.Mark
		}
	}
	return strings.Join(segments, "/")
}

// maskURL spells the address raw with its user information, each query
// value and each capability in its path masked, and answers only its host
// when it does not parse.
func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return hostOf(raw)
	}
	var b strings.Builder
	b.WriteString(u.Scheme + "://")
	if u.User != nil {
		b.WriteString("***@")
	}
	b.WriteString(u.Host + maskPath(u.EscapedPath()))
	if u.RawQuery != "" {
		keys := sortedKeys(u.Query())
		for i, k := range keys {
			keys[i] = k + "=***"
		}
		b.WriteString("?" + strings.Join(keys, "&"))
	}
	return b.String()
}

// sha256Hex is the hex sha256 of s.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// unusualHost answers how a sentence names the class of host strazad
// should not reach, and the class in one word, or two empty strings.
func unusualHost(host string) (phrase, class string) {
	switch cls := hostClass(host); cls {
	case "":
	case hostUnspecified:
		return "an unspecified address", cls
	default:
		return "a " + cls + " address", cls
	}
	if u, ok := unicodeHost(host); ok {
		return "a name with a punycode label that reads " + u, "punycode"
	}
	return "", ""
}

// dialNote appends what strazad does with a host class its dial guard
// refuses, so the risk sentence says the server would not answer.
func dialNote(class string) string {
	switch class {
	case hostUnspecified, "link-local", "cloud metadata":
		return " Straza does not dial such an address, so the server would stay degraded until its address changes."
	case "loopback":
		return " Straza dials a loopback address only where apps.allowLoopbackUpstreams is set."
	}
	return ""
}

// agentsRisk answers server.agents-credential when the draft lets agents
// without a sign-in of their own use the shared account, their sponsor's
// connection, or a token of their own client, or moves that client to
// another provider.
func agentsRisk(name string, live App, had bool, next App) []Finding {
	what, typed := "", name
	switch next.Agents {
	case AgentsShared:
		what = "the shared account of"
	case agentsSponsor:
		what = "their sponsor's connection to"
	case credentialClientCredentials:
		what, typed = "a token of their own client at the provider "+next.Provider+" for", next.Provider
	default:
		return nil
	}
	if had && live.Agents == next.Agents && live.Provider == next.Provider {
		return nil
	}
	before := ""
	if had {
		before = strings.TrimSpace(live.Agents + " " + live.Provider)
	}
	return []Finding{risk(codeAgentsCred, "App/"+name, typed, fmt.Sprintf("Agents with no sign-in of their own will use %s %s.", what, name),
		before, strings.TrimSpace(next.Agents+" "+next.Provider))}
}

// sharedRisks answers the risks of a live server the draft changes in
// place: tools its exposure newly offers, and a caller's own credential
// swapped for one shared account. The exposure is compared on every put:
// by the tools the server offers when a list is known for what it will
// run, and by the globs alone when none is, as after a new address.
func sharedRisks(name string, live, next App) []Finding {
	var out []Finding
	object := "App/" + name
	was, will := exposureOf(live), exposureOf(next)
	var more []string
	for _, t := range next.Offered {
		if matchAnyGlob(will, t) && !matchAnyGlob(was, t) {
			more = append(more, t)
		}
	}
	sort.Strings(more)
	switch {
	case len(more) > 0:
		out = append(out, risk(codeExposureWider, object, "", fmt.Sprintf("%s will offer more of its tools, among them %s.", name, listWords(more[:min(3, len(more))], "and")),
			strings.Join(sortedCopy(was), ", "), strings.Join(sortedCopy(will), ", ")))
	case next.Offered == nil && admitsMore(was, will):
		out = append(out, risk(codeExposureWider, object, "", fmt.Sprintf("%s will offer more of its tools, because its exposure widens from %s to %s.", name,
			listWords(sortedCopy(was), "and"), listWords(sortedCopy(will), "and")), strings.Join(sortedCopy(was), ", "), strings.Join(sortedCopy(will), ", ")))
	}
	if (live.Credential == CredentialToken || live.Credential == CredentialOAuth) && next.Credential == credentialStatic {
		out = append(out, risk(codeSharedAccount, object, "", fmt.Sprintf(
			"Calls to %s will use one shared account instead of each person's own sign-in, so %s sees one identity.", name, name), live.Credential, "shared"))
	}
	return out
}

// scopesRisk answers server.scopes-wider when a live server comes to ask,
// in every person's sign-in at its OAuth provider, for a scope it does not
// ask for there today. A scope at another provider counts as new.
func scopesRisk(name string, live, next App) []Finding {
	if next.Credential != CredentialOAuth {
		return nil
	}
	var today []string
	if live.Credential == CredentialOAuth && live.Provider == next.Provider {
		today = live.Scopes
	}
	var added []string
	for _, s := range sortedCopy(next.Scopes) {
		if !slices.Contains(today, s) && !slices.Contains(added, s) {
			added = append(added, s)
		}
	}
	if len(added) == 0 {
		return nil
	}
	what := "the scope " + added[0]
	if len(added) > 1 {
		what = "the scopes " + listWords(added, "and")
	}
	return []Finding{risk(codeScopesWider, "App/"+name, name, fmt.Sprintf(
		"%s will ask for %s in every person's sign-in at %s, beyond what it asks for today, so the tokens Straza holds for %s can do more.", name, what, next.Provider, name),
		strings.Join(sortedCopy(live.Scopes), ", "), strings.Join(sortedCopy(next.Scopes), ", "))}
}

// exposureOf answers the exposure globs of app, every tool when it names
// none.
func exposureOf(app App) []string {
	if len(app.Exposure) == 0 {
		return []string{"*"}
	}
	return app.Exposure
}
