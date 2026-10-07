// Package e2ematrix runs the spec-authored journey corpus under corpus/
// against binaries built from the tree: a real strazad, the real straza
// client deciding every hook event in each harness dialect, the real
// gateway, and the admin and SCIM APIs. README.md next to this package is
// the contract the runner implements.
package e2ematrix

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// dialects lists the hook dialects a scenario may name under harnesses.
var dialects = []string{"claude-code", "codex", "gemini", "python-sdk"}

// actionNames lists the step actions, in the order the README documents them.
var actionNames = []string{"admin", "scim", "enroll", "approver", "daemon", "server", "hook", "mcp", "audit", "wait"}

// scenario is one corpus file.
type scenario struct {
	ID         string   `yaml:"id"`
	Group      string   `yaml:"group"`
	Title      string   `yaml:"title"`
	Provenance string   `yaml:"provenance"`
	Harnesses  []string `yaml:"harnesses"`
	// Governance holds the lifetimes the scenario's strazad boots with, as
	// duration strings under the keys of the config file's governance
	// section. Only the three lifetimes are accepted; see validateGovernance.
	Governance map[string]string   `yaml:"governance"`
	Identities map[string]identity `yaml:"identities"`
	Steps      []step              `yaml:"steps"`
	// Suspect lists the steps whose expected value the sources left open.
	// The runner parses it and does nothing with it.
	Suspect any    `yaml:"suspect"`
	File    string `yaml:"-"`
}

// identity declares a participant: a human with the password the built-in
// issuer accepts, or an NHI that enrols headless with a local key.
type identity struct {
	Kind     string `yaml:"kind"`
	Password string `yaml:"password"`
}

// step is one scenario step: a name, exactly one action with its fields, and
// the optional want, save, within and timer keys.
type step struct {
	Name   string
	Action string
	Args   map[string]any
	Want   map[string]any
	Save   map[string]string
	Within time.Duration
	Timer  string
	// Wait is the sleep of a wait step, the one action written as a plain
	// duration rather than a map of fields.
	Wait time.Duration
}

// UnmarshalYAML reads the step's map form and picks its single action key.
func (s *step) UnmarshalYAML(n *yaml.Node) error {
	var raw map[string]any
	if err := n.Decode(&raw); err != nil {
		return err
	}
	return s.fromMap(raw)
}

func (s *step) fromMap(raw map[string]any) error {
	s.Name, _ = raw["name"].(string)
	if s.Name == "" {
		return errors.New("step without a name")
	}
	for _, a := range actionNames {
		if _, ok := raw[a]; !ok {
			continue
		}
		if s.Action != "" {
			return fmt.Errorf("step %q names two actions (%s and %s)", s.Name, s.Action, a)
		}
		s.Action = a
		if a == "wait" {
			d, err := time.ParseDuration(fmt.Sprint(raw[a]))
			if err != nil || d <= 0 {
				return fmt.Errorf("step %q: wait must be a positive duration such as 1m5s, got %v", s.Name, raw[a])
			}
			s.Wait, s.Args = d, map[string]any{}
			continue
		}
		args, ok := raw[a].(map[string]any)
		if !ok {
			return fmt.Errorf("step %q: %s must be a map", s.Name, a)
		}
		s.Args = args
	}
	if s.Action == "" {
		return fmt.Errorf("step %q has no action (one of %s)", s.Name, strings.Join(actionNames, ", "))
	}
	for k, v := range raw {
		switch k {
		case "name", s.Action:
		case "want":
			if v == nil {
				continue
			}
			m, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("step %q: want must be a map", s.Name)
			}
			s.Want = m
		case "save":
			m, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("step %q: save must be a map of name to json path", s.Name)
			}
			s.Save = map[string]string{}
			for name, p := range m {
				ps, ok := p.(string)
				if !ok || ps == "" {
					return fmt.Errorf("step %q: save.%s must be a json path", s.Name, name)
				}
				s.Save[name] = ps
			}
		case "within":
			d, err := time.ParseDuration(fmt.Sprint(v))
			if err != nil || d <= 0 {
				return fmt.Errorf("step %q: within must be a positive duration, got %v", s.Name, v)
			}
			s.Within = d
		case "timer":
			s.Timer, _ = v.(string)
			if s.Timer == "" {
				return fmt.Errorf("step %q: timer must be a name", s.Name)
			}
		default:
			return fmt.Errorf("step %q: unknown key %q", s.Name, k)
		}
	}
	return nil
}

// str returns a string field of the action's arguments.
func (s *step) str(key string) string {
	v, _ := s.Args[key].(string)
	return v
}

// wantStr returns a string field of want.
func (s *step) wantStr(key string) string {
	v, _ := s.Want[key].(string)
	return v
}

// eventKind returns a hook step's canonical event kind, empty for a raw
// payload step.
func (s *step) eventKind() string {
	ev, _ := s.Args["event"].(map[string]any)
	kind, _ := ev["kind"].(string)
	return kind
}

// event is a canonical hook event in the hook-profile vocabulary.
type event struct {
	Kind     string
	Tool     string
	Command  string
	Path     string
	Content  string
	App      string
	ToolName string
	Args     map[string]any
	Prompt   string
	Output   string
}

var eventKinds = map[string]bool{"session.start": true, "prompt.submit": true, "tool.pre": true, "tool.post": true, "session.end": true}

var toolKinds = map[string]bool{"shell.exec": true, "file.read": true, "file.write": true, "mcp.call": true}

// eventFromMap reads a hook step's event field.
func eventFromMap(m map[string]any) (event, error) {
	get := func(k string) string {
		v, _ := m[k].(string)
		return v
	}
	ev := event{Kind: get("kind"), Tool: get("tool"), Command: get("command"), Path: get("path"),
		Content: get("content"), App: get("app"), ToolName: get("toolName"), Prompt: get("prompt"), Output: get("output")}
	if args, ok := m["args"].(map[string]any); ok {
		ev.Args = args
	}
	if !eventKinds[ev.Kind] {
		return ev, fmt.Errorf("event.kind %q is not a canonical event", ev.Kind)
	}
	if ev.Kind == "tool.pre" || ev.Kind == "tool.post" {
		if !toolKinds[ev.Tool] {
			return ev, fmt.Errorf("event.tool %q is not a canonical tool", ev.Tool)
		}
		switch ev.Tool {
		case "shell.exec":
			if ev.Command == "" {
				return ev, errors.New("shell.exec needs command")
			}
		case "file.read", "file.write":
			if ev.Path == "" {
				return ev, fmt.Errorf("%s needs path", ev.Tool)
			}
		case "mcp.call":
			if ev.App == "" || ev.ToolName == "" {
				return ev, errors.New("mcp.call needs app and toolName")
			}
		}
	}
	return ev, nil
}

// loadCorpus reads every scenario file under dir in name order and refuses an
// empty directory, a file that fails validation, and a duplicate id.
func loadCorpus(dir string) ([]scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("corpus directory %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := filepath.Ext(e.Name()); ext == ".yaml" || ext == ".yml" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("corpus is empty: no scenario file under %s", dir)
	}
	seen := map[string]string{}
	var out []scenario
	for _, name := range names {
		path := filepath.Join(dir, name)
		sc, err := loadScenario(path)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[sc.ID]; dup {
			return nil, fmt.Errorf("%s: id %q is already used by %s", name, sc.ID, prev)
		}
		seen[sc.ID] = name
		out = append(out, sc)
	}
	return out, nil
}

// loadScenario parses and validates one scenario file.
func loadScenario(path string) (scenario, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- a corpus file under this package
	if err != nil {
		return scenario{}, err
	}
	var sc scenario
	if err := yaml.Unmarshal(raw, &sc); err != nil {
		return scenario{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	sc.File = filepath.Base(path)
	if err := sc.validate(); err != nil {
		return scenario{}, fmt.Errorf("%s: %w", sc.File, err)
	}
	return sc, nil
}

func (sc *scenario) validate() error {
	if sc.ID == "" || sc.Group == "" || sc.Title == "" {
		return errors.New("id, group and title are required")
	}
	if base := strings.TrimSuffix(strings.TrimSuffix(sc.File, ".yaml"), ".yml"); base != sc.ID {
		return fmt.Errorf("id %q must equal the file name %q (corpus files are named <group>-<slug>.yaml)", sc.ID, base)
	}
	if !strings.HasPrefix(sc.Provenance, "doc ") && !strings.HasPrefix(sc.Provenance, "live ") {
		return errors.New("no provenance tag: the scenario needs a provenance line that starts with \"doc \" or \"live \" and names what it proves")
	}
	seen := map[string]bool{}
	for _, h := range sc.Harnesses {
		if !isDialect(h) {
			return fmt.Errorf("harness %q is not one of %s", h, strings.Join(dialects, ", "))
		}
		if seen[h] {
			return fmt.Errorf("harness %q listed twice", h)
		}
		seen[h] = true
	}
	for name, id := range sc.Identities {
		switch id.Kind {
		case "human":
			if id.Password == "" {
				return fmt.Errorf("identity %s: a human carries the password the built-in issuer accepts", name)
			}
		case "nhi":
		default:
			return fmt.Errorf("identity %s: kind must be human or nhi", name)
		}
	}
	if err := sc.validateGovernance(); err != nil {
		return err
	}
	if len(sc.Steps) == 0 {
		return errors.New("a scenario needs at least one step")
	}
	for i := range sc.Steps {
		if err := sc.validateStep(&sc.Steps[i]); err != nil {
			return fmt.Errorf("step %q: %w", sc.Steps[i].Name, err)
		}
	}
	return nil
}

// governanceKeys are the config-file lifetimes a scenario may shorten, and
// nothing else: a posture knob such as localToolDefault or offlineGraceTTL
// set to hours would change what the scenario proves without a reader
// seeing it in the steps.
var governanceKeys = []string{"offlineGraceTTL", "deviceTokenTTL", "sessionMaxLifetime"}

// validateGovernance accepts the three lifetimes as durations: the grace
// bound may be zero, the profile value the enterprise contract states, and
// the two credential lifetimes must be positive, as the server demands.
func (sc *scenario) validateGovernance() error {
	for key, raw := range sc.Governance {
		if !contains(governanceKeys, key) {
			return fmt.Errorf("governance.%s is not one of %s", key, strings.Join(governanceKeys, ", "))
		}
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 || (d == 0 && key != "offlineGraceTTL") {
			return fmt.Errorf("governance.%s must be a positive duration such as 3m30s, got %q", key, raw)
		}
	}
	return nil
}

func (sc *scenario) validateStep(st *step) error {
	needIdentity := func() error {
		as := st.str("as")
		if _, ok := sc.Identities[as]; !ok {
			return fmt.Errorf("as %q is not a declared identity", as)
		}
		return nil
	}
	switch st.Action {
	case "admin", "scim":
		if st.str("method") == "" || st.str("path") == "" {
			return errors.New("method and path are required")
		}
		if _, ok := st.Args["as"]; !ok {
			return nil
		}
		if st.Action == "scim" {
			return errors.New("as belongs on an admin step, because a scim step always acts as the IdM")
		}
		if err := needIdentity(); err != nil {
			return err
		}
		if sc.Identities[st.str("as")].Kind != "human" {
			return fmt.Errorf("as %q must be a human identity, because the step signs that person in with their password and a non-human identity never decides", st.str("as"))
		}
	case "wait":
		if st.Want != nil || st.Save != nil || st.Within != 0 || st.Timer != "" {
			return errors.New("a wait records nothing, so it takes no want, save, within or timer: a value a poll can observe is written with within instead")
		}
	case "enroll":
		return needIdentity()
	case "approver":
		return sc.validateApprover(st, needIdentity)
	case "daemon":
		if err := needIdentity(); err != nil {
			return err
		}
		if a := st.str("action"); a != "start" && a != "stop" {
			return errors.New("daemon.action must be start or stop")
		}
	case "server":
		if a := st.str("action"); a != "start" && a != "stop" {
			return errors.New("server.action must be start or stop")
		}
	case "hook":
		return sc.validateHook(st)
	case "mcp":
		if err := needIdentity(); err != nil {
			return err
		}
		_, list := st.Args["list"]
		_, tool := st.Args["tool"]
		if list == tool {
			return errors.New("mcp needs exactly one of list: true or a tool")
		}
	case "audit":
		if len(st.Args) == 0 {
			return errors.New("audit needs at least one of subject, kind, action, decision, reasonContains, count")
		}
	}
	return nil
}

// validateApprover checks the two verbs of the approver action. Only a human
// signs, because a non-human identity never decides. A decide takes no
// within: its challenge is single use, so a repeat after a success would
// answer 409 and hide the first answer.
func (sc *scenario) validateApprover(st *step, needIdentity func() error) error {
	if err := needIdentity(); err != nil {
		return err
	}
	if sc.Identities[st.str("as")].Kind != "human" {
		return fmt.Errorf("as %q must be a human identity, because a non-human identity never decides", st.str("as"))
	}
	switch st.str("action") {
	case "enroll":
		if st.str("token") == "" {
			return errors.New("approver enroll needs token, the enroll token an earlier step saved")
		}
	case "decide":
		if st.str("request") == "" {
			return errors.New("approver decide needs request, the id of the approval request")
		}
		if v := st.str("verdict"); v != "approve" && v != "deny" {
			return errors.New("approver decide needs verdict approve or deny")
		}
		if st.Within != 0 {
			return errors.New("an approver decide takes no within, because its challenge is single use and a repeat would answer 409")
		}
	default:
		return errors.New("approver.action must be enroll or decide")
	}
	return nil
}

func (sc *scenario) validateHook(st *step) error {
	if _, ok := sc.Identities[st.str("as")]; !ok {
		return fmt.Errorf("as %q is not a declared identity", st.str("as"))
	}
	ev, hasEvent := st.Args["event"].(map[string]any)
	payload, hasPayload := st.Args["payload"].(map[string]any)
	if hasEvent == hasPayload {
		return errors.New("hook needs exactly one of event or payload")
	}
	if hasEvent {
		if _, err := eventFromMap(ev); err != nil {
			return err
		}
	}
	if hasPayload {
		for d := range payload {
			if !isDialect(d) {
				return fmt.Errorf("payload names unknown dialect %q", d)
			}
			if !contains(sc.Harnesses, d) {
				return fmt.Errorf("payload names dialect %q that harnesses does not list", d)
			}
		}
	}
	if d := st.wantStr("decision"); d != "" && d != "allow" && d != "deny" && d != "block" {
		return fmt.Errorf("want.decision %q must be allow, deny or block", d)
	}
	return nil
}

// runsUnder reports whether a hook step runs under harness: every event
// step does, a raw payload step only under the dialects it names.
func (s *step) runsUnder(harness string) bool {
	payload, ok := s.Args["payload"].(map[string]any)
	if !ok {
		return true
	}
	_, named := payload[harness]
	return named
}

func isDialect(d string) bool { return contains(dialects, d) }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

var placeholderRE = regexp.MustCompile(`\$\{([A-Za-z0-9_.-]+)\}`)

// expand substitutes ${name} in every string of v, walking maps and lists.
// An unknown name is an error so a typo never reaches the server as text.
func expand(v any, vars map[string]string) (any, error) {
	switch t := v.(type) {
	case string:
		var missing string
		out := placeholderRE.ReplaceAllStringFunc(t, func(m string) string {
			name := m[2 : len(m)-1]
			val, ok := vars[name]
			if !ok {
				missing = name
				return m
			}
			return val
		})
		if missing != "" {
			return nil, fmt.Errorf("unknown placeholder ${%s}", missing)
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			e, err := expand(val, vars)
			if err != nil {
				return nil, err
			}
			out[k] = e
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			e, err := expand(val, vars)
			if err != nil {
				return nil, err
			}
			out[i] = e
		}
		return out, nil
	default:
		return v, nil
	}
}
