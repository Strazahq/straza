// Package manager implements the MCP manager: app manifest parsing,
// runtimes, the GitOps watcher, and app lifecycle with tool-inventory drift
// detection.
//
// Wire format authority: spec/app-manifest (schema + SPEC.md). Tests consume
// the spec examples and the registry-import fixtures directly.
package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/redact"
)

// APIVersion is the canonical document version (spec/app-manifest v1beta1).
// New manifests MUST declare it, and registry import emits it. An id under
// any other domain is refused.
const APIVersion = "straza.dev/v1beta1"

// Runtime kinds.
const (
	RuntimeCommand = "command"
	RuntimeRemote  = "remote"
	RuntimeOCI     = "oci"
)

// Credential kinds and injection modes. The two caller kinds, oauth
// and token, give each caller their own credential and are valid on remote
// runtimes only: a command or oci server is one process and one identity.
const (
	CredentialNone   = "none"
	CredentialStatic = "static"
	CredentialOAuth  = "oauth"
	CredentialToken  = "token"

	// Agents values say what an agent with no row of its own uses on a
	// caller-kind app: nothing, its sponsor's row when the sponsor allowed
	// it, the server's shared row, or a token of its own client at the
	// server's OAuth provider (kind oauth only).
	AgentsOwn               = "own"
	AgentsSponsor           = "sponsor"
	AgentsShared            = "shared"
	AgentsClientCredentials = "client_credentials"

	InjectHeader = "header"
	InjectEnv    = "env"

	AuthInject      = "inject"
	AuthPassthrough = "passthrough"

	// SecretPlaceholder is the substring every injection template must
	// contain. The broker replaces it with the decrypted secret.
	SecretPlaceholder = "{{secret}}"
)

var (
	nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)
	cpuRe  = regexp.MustCompile(`^[0-9]+m?$`)
	memRe  = regexp.MustCompile(`^[0-9]+(Ki|Mi|Gi)$`)
)

// Manifest is a parsed App document (spec/app-manifest v1beta1).
type Manifest struct {
	APIVersion string         `yaml:"apiVersion" json:"apiVersion"`
	Kind       string         `yaml:"kind" json:"kind"`
	Metadata   Metadata       `yaml:"metadata" json:"metadata"`
	Server     map[string]any `yaml:"server" json:"server"` // verbatim registry server.json
	Straza     Extensions     `yaml:"straza" json:"straza"`
}

// Metadata names the app. Name is the deployment-unique policy identity and
// the gateway namespace prefix (`<name>__<tool>`).
type Metadata struct {
	Name        string `yaml:"name" json:"name"`
	Namespace   string `yaml:"namespace,omitempty" json:"namespace,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Extensions is the `straza:` block.
type Extensions struct {
	Runtime    RuntimeSpec     `yaml:"runtime" json:"runtime"`
	Credential *CredentialSpec `yaml:"credential,omitempty" json:"credential,omitempty"`
	Exposure   *ExposureSpec   `yaml:"exposure,omitempty" json:"exposure,omitempty"`
	Limits     *LimitsSpec     `yaml:"limits,omitempty" json:"limits,omitempty"`
}

// RuntimeSpec selects exactly one runtime block (SPEC.md §2).
type RuntimeSpec struct {
	Kind    string       `yaml:"kind" json:"kind"`
	Command *CommandSpec `yaml:"command,omitempty" json:"command,omitempty"`
	Remote  *RemoteSpec  `yaml:"remote,omitempty" json:"remote,omitempty"`
	OCI     *OCISpec     `yaml:"oci,omitempty" json:"oci,omitempty"`
}

// CommandSpec runs a child process speaking MCP over stdio.
type CommandSpec struct {
	Exec    string   `yaml:"exec" json:"exec"`
	Args    []string `yaml:"args,omitempty" json:"args,omitempty"`
	Env     []EnvVar `yaml:"env,omitempty" json:"env,omitempty"`
	Workdir string   `yaml:"workdir,omitempty" json:"workdir,omitempty"`
}

// EnvVar is one fixed environment entry.
type EnvVar struct {
	Name  string `yaml:"name" json:"name"`
	Value string `yaml:"value" json:"value"`
}

// RemoteSpec proxies to a remote streamable-HTTP MCP server.
type RemoteSpec struct {
	URL  string `yaml:"url" json:"url"`
	Auth string `yaml:"auth,omitempty" json:"auth,omitempty"` // inject|passthrough (default inject)
}

// OCISpec runs a container image.
type OCISpec struct {
	Image   string   `yaml:"image" json:"image"`
	Sandbox string   `yaml:"sandbox,omitempty" json:"sandbox,omitempty"` // default|none
	Env     []EnvVar `yaml:"env,omitempty" json:"env,omitempty"`
}

// CredentialSpec declares how the broker injects the upstream secret.
type CredentialSpec struct {
	Kind   string      `yaml:"kind,omitempty" json:"kind,omitempty"`     // none|static|oauth|token
	Agents string      `yaml:"agents,omitempty" json:"agents,omitempty"` // own|sponsor|shared|client_credentials, caller kinds only
	OAuth  *OAuthSpec  `yaml:"oauth,omitempty" json:"oauth,omitempty"`
	Inject *InjectSpec `yaml:"inject,omitempty" json:"inject,omitempty"`
}

// OAuthSpec is the per-user connect declaration.
type OAuthSpec struct {
	Provider string   `yaml:"provider" json:"provider"`
	Scopes   []string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
}

// InjectSpec renders the secret gateway-side. The result never reaches a
// client.
type InjectSpec struct {
	As       string `yaml:"as" json:"as"` // header|env
	Name     string `yaml:"name" json:"name"`
	Template string `yaml:"template,omitempty" json:"template,omitempty"` // default "{{secret}}"
}

// ExposureSpec is the manager-level tool cap (glob list, default ["*"]).
type ExposureSpec struct {
	Tools []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	// Views lets the server show its MCP Apps views: Straza tells the server
	// that hosts can render them and serves them on the server's own endpoint.
	Views bool `yaml:"views,omitempty" json:"views,omitempty"`
}

// LimitsSpec is always parsed and persisted. rps and timeoutSeconds are
// enforced, and cpu and mem are stored but not enforced.
type LimitsSpec struct {
	CPU string  `yaml:"cpu,omitempty" json:"cpu,omitempty"`
	Mem string  `yaml:"mem,omitempty" json:"mem,omitempty"`
	RPS float64 `yaml:"rps,omitempty" json:"rps,omitempty"`
	// TimeoutSeconds caps a single gateway→upstream tools/call for this app,
	// overriding the server-wide apps.upstreamTimeout (0 = use the global).
	TimeoutSeconds int `yaml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
}

// Parse strictly decodes and validates one App YAML document, returning it
// with defaults applied (credential kind → none, remote auth → inject, oci
// sandbox → default, inject template → "{{secret}}", exposure → ["*"]).
// The validator MUST agree with spec/app-manifest/app-manifest.schema.json
// on the example corpus, enforced by TestManifestAgreesWithSpecExamples.
func Parse(raw []byte) (Manifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("manifest: parse: %w", err)
	}
	if err := validate(&m); err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	if err := storable(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// storable refuses a manifest whose stored JSON form cannot be produced,
// so the operator meets it as a manifest error and not as a failed install.
// yaml lets through two things JSON cannot carry: a mapping key that is not
// a string, which only the free-form server block can hold, and an
// infinite or NaN number.
func storable(m Manifest) error {
	_, err := json.Marshal(m)
	var typeErr *json.UnsupportedTypeError
	var valueErr *json.UnsupportedValueError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &typeErr):
		return errors.New("manifest: the server block holds a key that is not a string, which cannot be stored. Quote the key, then install again")
	case errors.As(err, &valueErr):
		return fmt.Errorf("manifest: the number %s cannot be stored. Write a finite number, then install again", valueErr.Str)
	default:
		return fmt.Errorf("manifest: the manifest cannot be stored: %w", err)
	}
}

// ServerName returns server.name (guaranteed a non-empty string after Parse).
func (m Manifest) ServerName() string { v, _ := m.Server["name"].(string); return v }

// ServerVersion returns server.version (guaranteed non-empty after Parse).
func (m Manifest) ServerVersion() string { v, _ := m.Server["version"].(string); return v }

// ExposedTools returns the manager-level tool cap globs.
func (m Manifest) ExposedTools() []string {
	if m.Straza.Exposure == nil || len(m.Straza.Exposure.Tools) == 0 {
		return []string{"*"}
	}
	return m.Straza.Exposure.Tools
}

// CredentialKind returns the effective credential kind (default none).
func (m Manifest) CredentialKind() string {
	if m.Straza.Credential == nil || m.Straza.Credential.Kind == "" {
		return CredentialNone
	}
	return m.Straza.Credential.Kind
}

// CallerKind reports whether the app gives each caller their own credential
// (kind oauth or token).
func (m Manifest) CallerKind() bool {
	k := m.CredentialKind()
	return k == CredentialOAuth || k == CredentialToken
}

// AgentsSource returns the effective credential.agents value for a
// caller-kind app (default own) and the empty string for every other kind.
func (m Manifest) AgentsSource() string {
	if !m.CallerKind() {
		return ""
	}
	if m.Straza.Credential.Agents == "" {
		return AgentsOwn
	}
	return m.Straza.Credential.Agents
}

// JSON returns the manifest as canonical JSON for persistence
// (store.App.Manifest). The server block round-trips as data (SPEC.md §1).
func (m Manifest) JSON() (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("manifest: encode: %w", err)
	}
	return string(b), nil
}

// FromJSON re-opens a persisted manifest without re-validating (the stored
// copy already passed Parse).
func FromJSON(raw string) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return Manifest{}, fmt.Errorf("manifest: decode stored: %w", err)
	}
	applyDefaults(&m)
	return m, nil
}

func applyDefaults(m *Manifest) {
	if m.Straza.Credential != nil && m.Straza.Credential.Kind == "" {
		m.Straza.Credential.Kind = CredentialNone
	}
	if m.Straza.Credential != nil && m.Straza.Credential.Inject != nil && m.Straza.Credential.Inject.Template == "" {
		m.Straza.Credential.Inject.Template = SecretPlaceholder
	}
	if m.CallerKind() && m.Straza.Credential.Agents == "" {
		m.Straza.Credential.Agents = AgentsOwn
	}
	if m.Straza.Runtime.Remote != nil && m.Straza.Runtime.Remote.Auth == "" {
		m.Straza.Runtime.Remote.Auth = AuthInject
	}
	if m.Straza.Runtime.OCI != nil && m.Straza.Runtime.OCI.Sandbox == "" {
		m.Straza.Runtime.OCI.Sandbox = "default"
	}
	if m.Straza.Exposure == nil {
		m.Straza.Exposure = &ExposureSpec{Tools: []string{"*"}}
	}
}

func validate(m *Manifest) error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if m.APIVersion != APIVersion {
		fail("apiVersion must be %q, got %q", APIVersion, m.APIVersion)
	}
	if m.Kind != "App" {
		fail("kind must be App, got %q", m.Kind)
	}
	if !nameRe.MatchString(m.Metadata.Name) {
		fail("metadata.name %q must match %s", m.Metadata.Name, nameRe)
	}
	if m.Server == nil {
		fail("server block is required (verbatim registry server.json)")
	} else {
		if v, ok := m.Server["name"].(string); !ok || v == "" {
			fail("server.name must be a non-empty string")
		}
		if v, ok := m.Server["version"].(string); !ok || v == "" {
			fail("server.version must be a non-empty string")
		}
	}

	validateRuntime(&m.Straza.Runtime, fail)
	validateCredential(m, fail)

	if m.Straza.Exposure != nil {
		switch {
		case len(m.Straza.Exposure.Tools) == 0 && m.Straza.Exposure.Views:
			fail(`exposure.views needs exposure.tools beside it. Write tools: ["*"] to keep every tool of the server`)
		case len(m.Straza.Exposure.Tools) == 0:
			fail("exposure.tools must not be empty when present")
		}
		for _, t := range m.Straza.Exposure.Tools {
			if t == "" {
				fail("exposure.tools entries must be non-empty")
			}
		}
	}
	if l := m.Straza.Limits; l != nil {
		if l.CPU != "" && !cpuRe.MatchString(l.CPU) {
			fail("limits.cpu %q must match %s", l.CPU, cpuRe)
		}
		if l.Mem != "" && !memRe.MatchString(l.Mem) {
			fail("limits.mem %q must match %s", l.Mem, memRe)
		}
		if l.RPS < 0 {
			fail("limits.rps must be >= 0")
		}
		if l.TimeoutSeconds < 0 {
			fail("limits.timeoutSeconds must be >= 0")
		}
	}

	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return fmt.Errorf("invalid App %q: %s", m.Metadata.Name, strings.Join(msgs, "; "))
	}
	applyDefaults(m)
	return nil
}

func validateRuntime(r *RuntimeSpec, fail func(string, ...any)) {
	blocks := map[string]bool{
		RuntimeCommand: r.Command != nil,
		RuntimeRemote:  r.Remote != nil,
		RuntimeOCI:     r.OCI != nil,
	}
	if _, known := blocks[r.Kind]; !known {
		fail("runtime.kind must be command|remote|oci, got %q", r.Kind)
		return
	}
	for kind, present := range blocks {
		if kind == r.Kind && !present {
			fail("runtime.kind %s requires a runtime.%s block", kind, kind)
		}
		if kind != r.Kind && present {
			fail("runtime.%s block conflicts with runtime.kind %s", kind, r.Kind)
		}
	}
	switch {
	case r.Kind == RuntimeCommand && r.Command != nil:
		if r.Command.Exec == "" {
			fail("runtime.command.exec is required")
		}
		validateEnv(r.Command.Env, "runtime.command", fail)
	case r.Kind == RuntimeRemote && r.Remote != nil:
		if !strings.HasPrefix(r.Remote.URL, "http://") && !strings.HasPrefix(r.Remote.URL, "https://") {
			fail("runtime.remote.url %q must be http(s)", redact.URL(r.Remote.URL))
		}
		if a := r.Remote.Auth; a != "" && a != AuthInject && a != AuthPassthrough {
			fail("runtime.remote.auth must be inject|passthrough, got %q", a)
		}
	case r.Kind == RuntimeOCI && r.OCI != nil:
		if r.OCI.Image == "" {
			fail("runtime.oci.image is required")
		}
		if s := r.OCI.Sandbox; s != "" && s != "default" && s != "none" {
			fail("runtime.oci.sandbox must be default|none, got %q", s)
		}
		validateEnv(r.OCI.Env, "runtime.oci", fail)
	}
}

func validateEnv(env []EnvVar, where string, fail func(string, ...any)) {
	for _, e := range env {
		if e.Name == "" {
			fail("%s.env entries require a name", where)
		}
	}
}

func validateCredential(m *Manifest, fail func(string, ...any)) {
	c := m.Straza.Credential
	kind := CredentialNone
	if c != nil && c.Kind != "" {
		kind = c.Kind
	}
	switch kind {
	case CredentialNone:
		if c != nil && c.Inject != nil {
			fail("credential.inject is forbidden with kind none")
		}
		if c != nil && c.OAuth != nil {
			fail("credential.oauth is forbidden with kind %q", kind)
		}
		if c != nil && c.Agents != "" {
			fail("credential.agents is only valid with kind oauth or token")
		}
		return
	case CredentialStatic:
		if c.OAuth != nil {
			fail("credential.oauth is forbidden with kind static")
		}
		if c.Agents != "" {
			fail("credential.agents is only valid with kind oauth or token")
		}
	case CredentialOAuth:
		if c.OAuth == nil || c.OAuth.Provider == "" {
			fail("credential.kind oauth requires oauth.provider")
		}
	case CredentialToken:
		if c.OAuth != nil {
			fail("credential.oauth is forbidden with kind token")
		}
	default:
		fail("credential.kind must be none|static|oauth|token, got %q", kind)
		return
	}
	if kind == CredentialOAuth || kind == CredentialToken {
		if rk := m.Straza.Runtime.Kind; rk != RuntimeRemote {
			fail("credential.kind %s gives each caller their own credential, which a %s runtime cannot take: it is one process and one identity. Use kind static, or run the server on your own machine", kind, rk)
		}
		switch c.Agents {
		case "", AgentsOwn, AgentsSponsor, AgentsShared:
		case AgentsClientCredentials:
			if kind != CredentialOAuth {
				fail("credential.agents client_credentials needs kind oauth, because the provider named under oauth.provider issues each agent's token. Use kind oauth, or set credential.agents to own, sponsor or shared")
			}
		default:
			fail("credential.agents must be own|sponsor|shared|client_credentials, got %q", c.Agents)
		}
	}

	in := c.Inject
	if in == nil {
		fail("credential.kind %s needs an inject block, because Straza must know where the secret goes on each call. Add credential.inject, for example as: header, name: Authorization, template: \"Bearer %s\" on a remote runtime, or as: env, name: API_TOKEN on a command or oci runtime", kind, SecretPlaceholder)
		return
	}
	if in.Name == "" {
		fail("credential.inject.name is empty, so Straza does not know which header or environment variable carries the secret. Set it, for example Authorization for a header or API_TOKEN for an environment variable")
	}
	if in.Template != "" && !strings.Contains(in.Template, SecretPlaceholder) {
		fail("credential.inject.template does not contain %[1]s, so the secret would never be sent. Put %[1]s where the secret goes, for example \"Bearer %[1]s\", or leave template out to send the secret as it is", SecretPlaceholder)
	}
	switch in.As {
	case InjectHeader:
		if m.Straza.Runtime.Kind != RuntimeRemote {
			fail("credential.inject.as header needs a remote runtime, because a command or oci runtime starts a process that reads an environment variable and gets no request headers. Use as: env with the variable name the process reads, for example API_TOKEN")
		}
	case InjectEnv:
		if m.Straza.Runtime.Kind == RuntimeRemote {
			fail("credential.inject.as env does not work on a remote runtime, because an HTTP server reads the secret from a request header and Straza starts no process there. Use as: header with the header name the server reads, for example Authorization")
		}
	default:
		fail("credential.inject.as must be header or env, got %q, so Straza does not know where the secret goes. Use header on a remote runtime, because an HTTP server reads a request header, or env on a command or oci runtime, because the process reads an environment variable", in.As)
	}
}
