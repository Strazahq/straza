package manager

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ImportOptions tune registry import (SPEC.md §5). Zero values follow the
// deterministic defaults.
type ImportOptions struct {
	// Name overrides the derived metadata.name.
	Name string
	// Runtime restricts selection to one runtime kind (command|remote|oci);
	// empty follows the precedence remote > oci > npm > pypi.
	Runtime string
}

// regServer is the typed view over a registry server.json used for runtime
// selection; the verbatim block is preserved separately.
type regServer struct {
	Name     string       `json:"name"`
	Version  string       `json:"version"`
	Packages []regPackage `json:"packages"`
	Remotes  []regRemote  `json:"remotes"`
}

type regPackage struct {
	RegistryType string `json:"registryType"`
	Identifier   string `json:"identifier"`
	Version      string `json:"version"`
	Transport    struct {
		Type string `json:"type"`
	} `json:"transport"`
	EnvironmentVariables []regKV `json:"environmentVariables"`
}

type regRemote struct {
	Type    string  `json:"type"`
	URL     string  `json:"url"`
	Headers []regKV `json:"headers"`
}

type regKV struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	IsSecret bool   `json:"isSecret"`
}

var placeholderRe = regexp.MustCompile(`\{[a-zA-Z0-9_.-]+\}`)

// Import converts one MCP-registry server.json object into a validated
// Manifest per the deterministic rules in spec/app-manifest SPEC.md §5.
// Fixtures: spec/conformance/registry/*.server.json → *.app.yaml.
func Import(serverJSON []byte, opts ImportOptions) (Manifest, error) {
	var verbatim map[string]any
	if err := json.Unmarshal(serverJSON, &verbatim); err != nil {
		return Manifest{}, fmt.Errorf("registry import: parse server.json: %w", err)
	}
	var reg regServer
	if err := json.Unmarshal(serverJSON, &reg); err != nil {
		return Manifest{}, fmt.Errorf("registry import: parse server.json: %w", err)
	}
	if reg.Name == "" || reg.Version == "" {
		return Manifest{}, fmt.Errorf("registry import: server.json requires name and version")
	}

	namespace, short := splitRegistryName(reg.Name)
	name := opts.Name
	if name == "" {
		name = sanitizeName(short)
	}
	if !nameRe.MatchString(name) {
		return Manifest{}, fmt.Errorf("registry import: derived name %q is not a valid app name (override with --name)", name)
	}

	m := Manifest{
		APIVersion: APIVersion,
		Kind:       "App",
		Metadata:   Metadata{Name: name, Namespace: namespace},
		Server:     verbatim,
	}
	if err := selectRuntime(&m, reg, opts.Runtime); err != nil {
		return Manifest{}, err
	}
	// Re-run full validation so import can never emit a manifest Parse would
	// reject.
	if err := validate(&m); err != nil {
		return Manifest{}, fmt.Errorf("registry import: %w", err)
	}
	return m, nil
}

// selectRuntime applies the precedence remote > oci > npm > pypi, restricted
// by an explicit kind when given.
func selectRuntime(m *Manifest, reg regServer, restrict string) error {
	if restrict != "" && restrict != RuntimeCommand && restrict != RuntimeRemote && restrict != RuntimeOCI {
		return fmt.Errorf("registry import: unknown runtime override %q", restrict)
	}

	if restrict == "" || restrict == RuntimeRemote {
		for _, r := range reg.Remotes {
			if r.Type != "streamable-http" {
				continue
			}
			m.Straza.Runtime = RuntimeSpec{Kind: RuntimeRemote, Remote: &RemoteSpec{URL: r.URL, Auth: AuthInject}}
			return applySecrets(m, r.Headers, InjectHeader)
		}
		if restrict == RuntimeRemote {
			return fmt.Errorf("registry import: no streamable-http remote in server.json")
		}
	}

	pick := func(registryType string) *regPackage {
		for i := range reg.Packages {
			p := &reg.Packages[i]
			if p.RegistryType == registryType && p.Transport.Type == "stdio" {
				return p
			}
		}
		return nil
	}

	if restrict == "" || restrict == RuntimeOCI {
		if p := pick("oci"); p != nil {
			m.Straza.Runtime = RuntimeSpec{Kind: RuntimeOCI, OCI: &OCISpec{Image: p.Identifier, Sandbox: "default"}}
			m.Straza.Runtime.OCI.Env = fixedEnv(p.EnvironmentVariables)
			return applySecrets(m, p.EnvironmentVariables, InjectEnv)
		}
		if restrict == RuntimeOCI {
			return fmt.Errorf("registry import: no stdio oci package in server.json")
		}
	}

	for _, cand := range []struct{ reg, exec, sep string }{
		{"npm", "npx", "@"},
		{"pypi", "uvx", "=="},
	} {
		p := pick(cand.reg)
		if p == nil {
			continue
		}
		version := p.Version
		if version == "" {
			version = reg.Version
		}
		ref := p.Identifier
		if version != "" {
			ref += cand.sep + version
		}
		args := []string{ref}
		if cand.exec == "npx" {
			args = []string{"-y", ref}
		}
		m.Straza.Runtime = RuntimeSpec{Kind: RuntimeCommand, Command: &CommandSpec{Exec: cand.exec, Args: args}}
		m.Straza.Runtime.Command.Env = fixedEnv(p.EnvironmentVariables)
		return applySecrets(m, p.EnvironmentVariables, InjectEnv)
	}
	if restrict == RuntimeCommand {
		return fmt.Errorf("registry import: no stdio npm/pypi package in server.json")
	}
	return fmt.Errorf("registry import: no usable runtime (need a streamable-http remote or a stdio oci/npm/pypi package)")
}

// fixedEnv maps non-secret variables with fixed values to runtime env entries.
func fixedEnv(vars []regKV) []EnvVar {
	var out []EnvVar
	for _, v := range vars {
		if !v.IsSecret && v.Value != "" {
			out = append(out, EnvVar{Name: v.Name, Value: v.Value})
		}
	}
	return out
}

// applySecrets maps at most one isSecret variable/header to a static
// credential with an injection template (SPEC.md §5 rule 5).
func applySecrets(m *Manifest, vars []regKV, as string) error {
	var secrets []regKV
	for _, v := range vars {
		if v.IsSecret {
			secrets = append(secrets, v)
		}
	}
	switch len(secrets) {
	case 0:
		return nil
	case 1:
		tmpl := SecretPlaceholder
		if v := secrets[0].Value; v != "" && placeholderRe.MatchString(v) {
			tmpl = placeholderRe.ReplaceAllString(v, SecretPlaceholder)
		}
		m.Straza.Credential = &CredentialSpec{
			Kind:   CredentialStatic,
			Inject: &InjectSpec{As: as, Name: secrets[0].Name, Template: tmpl},
		}
		return nil
	default:
		return fmt.Errorf("registry import: %d secret variables; v1beta1 supports exactly one", len(secrets))
	}
}

// splitRegistryName splits "namespace/short" on the last slash.
func splitRegistryName(full string) (namespace, short string) {
	if i := strings.LastIndex(full, "/"); i >= 0 {
		return full[:i], full[i+1:]
	}
	return "", full
}

// sanitizeName lowercases and maps everything outside [a-z0-9-] to '-',
// trimming leading/trailing dashes and truncating to the 64-char name limit.
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 64 {
		out = strings.TrimRight(out[:64], "-")
	}
	return out
}
