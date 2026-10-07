// Package conformance implements the spec conformance runner:
// it replays the published Tier-1 hook-profile corpus against an arbitrary
// hook implementation and scores the result. This is the artifact that lets
// a third party claim Tier-1 compatibility. Straza's own hook and the
// hello-hook reference implementation both pass it.
//
// Runner protocol (normative copy in spec/conformance/tier1/cases.yaml):
// per case the command under test is spawned with STRAZA_HARNESS set to the
// case's dialect and the payload on stdin; the decision is read back in the
// dialect's encoding. `{policy}` in the command line is replaced with the
// path of the materialized conformance PolicySet.
package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/spec/conformance/tier1"
)

// Suite mirrors spec/conformance/tier1/cases.yaml.
type Suite struct {
	Suite  string `yaml:"suite"`
	Policy string `yaml:"policy"`
	Cases  []Case `yaml:"cases"`
}

// Case is one replay: a dialect payload and the decision it must produce.
type Case struct {
	Name    string         `yaml:"name"`
	Harness string         `yaml:"harness"`
	Payload map[string]any `yaml:"payload"`
	Raw     string         `yaml:"raw"` // verbatim stdin (malformed-payload cases)
	Want    Want           `yaml:"want"`
	// Provenance names HOW this case was verified. "live ..." means captured
	// from or validated against the real harness binary (name it, with version
	// and date). "doc ..." means derived from vendor docs or schemas and not
	// confirmed against a binary. It is required: RunHookProfile refuses a
	// suite with an untagged case, so every case states how it was verified.
	Provenance string `yaml:"provenance"`
}

// Want is the expected outcome. Decision is allow, deny, or block (any
// refusal, used where the fail-closed encoding may legitimately vary).
// Silent additionally requires EMPTY stdout on an allow: the ack contract
// for claude-code non-enforceable events and every codex success. Both are
// verified against the binaries: claude 2.1.220 rejects a SessionEnd
// hookSpecificOutput document, and codex 0.146.0 strict-parses stdout per
// event and rejects even a well-formed permissionDecision:"allow". A hook
// that prints a decision document where the harness demands silence is
// broken in front of that harness, so the suite scores it broken here.
type Want struct {
	Decision       string `yaml:"decision"`
	ReasonContains string `yaml:"reasonContains"`
	Silent         bool   `yaml:"silent"`
}

// Result is one case's verdict; Detail explains a failure.
type Result struct {
	Name   string
	Pass   bool
	Detail string
}

// caseTimeout bounds one hook invocation; the hook-profile budget is
// milliseconds, so anything slower than this is broken, not slow.
const caseTimeout = 10 * time.Second

// RunHookProfile replays the embedded Tier-1 suite against cmdline, writing
// per-case verdicts and a score to out. It returns one Result per case; the
// error is reserved for infrastructure problems (unreadable suite, command
// not startable), never for case failures.
func RunHookProfile(cmdline string, out io.Writer) ([]Result, error) {
	rawSuite, err := tier1.FS.ReadFile("cases.yaml")
	if err != nil {
		return nil, fmt.Errorf("conformance: embedded suite missing: %w", err)
	}
	var suite Suite
	if err := yaml.Unmarshal(rawSuite, &suite); err != nil {
		return nil, fmt.Errorf("conformance: embedded suite invalid: %w", err)
	}
	for _, c := range suite.Cases {
		if !strings.HasPrefix(c.Provenance, "live ") && !strings.HasPrefix(c.Provenance, "doc ") {
			return nil, fmt.Errorf(
				"conformance: case %q has no provenance tag (want \"live ...\" or \"doc ...\"); how was it verified?", c.Name)
		}
	}
	pol, err := tier1.FS.ReadFile(suite.Policy)
	if err != nil {
		return nil, fmt.Errorf("conformance: embedded policy missing: %w", err)
	}
	polFile, err := os.CreateTemp("", "straza-tier1-*.yaml")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(polFile.Name()) }()
	if _, err := polFile.Write(pol); err != nil {
		_ = polFile.Close()
		return nil, err
	}
	if err := polFile.Close(); err != nil {
		return nil, err
	}

	argv := splitCommand(cmdline)
	if len(argv) == 0 {
		return nil, errors.New("conformance: empty --cmd")
	}
	for i := range argv {
		argv[i] = strings.ReplaceAll(argv[i], "{policy}", polFile.Name())
	}

	results := make([]Result, 0, len(suite.Cases))
	passed := 0
	for _, c := range suite.Cases {
		r := runCase(c, argv)
		if r.Pass {
			passed++
			fmt.Fprintf(out, "PASS %s\n", r.Name)
		} else {
			fmt.Fprintf(out, "FAIL %s: %s\n", r.Name, r.Detail)
		}
		results = append(results, r)
	}
	verdict := "PASS"
	if passed != len(results) {
		verdict = "FAIL"
	}
	fmt.Fprintf(out, "%s: %d/%d cases passed (Tier-1 conformance %s)\n",
		suite.Suite, passed, len(results), verdict)
	fmt.Fprintln(out, "note: this suite covers dialect normalization, decision encoding, and"+
		" fail-closed behavior; session-start context injection and audit spooling are not"+
		" externally observable and are the implementation's own test obligation.")
	return results, nil
}

// Passed reports whether every case passed.
func Passed(results []Result) bool {
	for _, r := range results {
		if !r.Pass {
			return false
		}
	}
	return len(results) > 0
}

func runCase(c Case, argv []string) Result {
	stdin := []byte(c.Raw)
	if c.Raw == "" {
		var err error
		stdin, err = json.Marshal(c.Payload)
		if err != nil {
			return Result{Name: c.Name, Detail: fmt.Sprintf("payload encode: %v", err)}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), caseTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- running the operator-supplied command IS the feature
	cmd.Env = append(os.Environ(), "STRAZA_HARNESS="+c.Harness)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	exit := 0
	if err != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.As(err, &exitErr):
			exit = exitErr.ExitCode()
		case ctx.Err() != nil:
			return Result{Name: c.Name, Detail: "timed out (10s). A hook must answer in milliseconds"}
		default:
			return Result{Name: c.Name, Detail: fmt.Sprintf("could not run command: %v", err)}
		}
	}
	pass, detail := judge(c, exit, stdout.Bytes(), stderr.Bytes())
	return Result{Name: c.Name, Pass: pass, Detail: detail}
}

// hookResponse is the union of the two decision encodings: gemini strict
// JSON (decision/reason) and the claude-code/codex hookSpecificOutput shape.
type hookResponse struct {
	Decision           string `json:"decision"`
	Reason             string `json:"reason"`
	HookSpecificOutput struct {
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// judge applies the decision-encoding contract from cases.yaml.
func judge(c Case, exit int, stdout, stderr []byte) (bool, string) {
	var resp hookResponse
	if trimmed := bytes.TrimSpace(stdout); len(trimmed) > 0 {
		_ = json.Unmarshal(trimmed, &resp) // non-JSON stdout = no decision document
	}
	denied := resp.Decision == "deny" || resp.HookSpecificOutput.PermissionDecision == "deny" ||
		(c.Harness != "gemini" && exit == 2)
	reason := resp.Reason
	if reason == "" {
		reason = resp.HookSpecificOutput.PermissionDecisionReason
	}
	if reason == "" {
		reason = strings.TrimSpace(string(stderr))
	}

	switch c.Want.Decision {
	case "allow":
		if exit != 0 {
			return false, fmt.Sprintf("want allow, got exit code %d (stderr: %s)", exit, stderr)
		}
		if denied {
			return false, "want allow, got a deny response"
		}
		if c.Want.Silent && len(bytes.TrimSpace(stdout)) > 0 {
			return false, fmt.Sprintf(
				"the ack for this event is EMPTY stdout. The harness strict-parses hook output and rejects a decision document here (claude 2.1.220 / codex 0.146.0, fired live); got %q", stdout)
		}
		return true, ""
	case "deny":
		if c.Harness == "gemini" && resp.Decision != "deny" {
			return false, fmt.Sprintf("gemini deny must be strict JSON {\"decision\":\"deny\"} on stdout, got %q (exit %d)", stdout, exit)
		}
		// Codex reads NOTHING but the exit code and stderr on a block: it
		// ignores stdout on exit 2 and rejects decision documents on exit 0.
		// A stdout-only deny fails OPEN in front of a real codex, so it does
		// not pass here either.
		if c.Harness == "codex" {
			if exit != 2 {
				return false, fmt.Sprintf("codex honors only exit code 2 as a block (stdout documents are ignored or rejected); got exit %d", exit)
			}
			if strings.TrimSpace(string(stderr)) == "" {
				return false, "codex reads the deny reason from stderr only; stderr is empty"
			}
			reason = strings.TrimSpace(string(stderr))
		}
		if !denied {
			return false, fmt.Sprintf("want deny, got allow (exit %d, stdout %q)", exit, stdout)
		}
		if reason == "" {
			return false, "deny carried no reason. The model cannot adapt without one"
		}
		if w := c.Want.ReasonContains; w != "" && !strings.Contains(strings.ToLower(reason), strings.ToLower(w)) {
			return false, fmt.Sprintf("deny reason %q does not mention %q", reason, w)
		}
		return true, ""
	case "block":
		if exit == 0 && !denied {
			return false, "malformed input must fail closed (non-zero exit or a deny response), got a clean allow"
		}
		return true, ""
	default:
		return false, fmt.Sprintf("suite bug: unknown want.decision %q", c.Want.Decision)
	}
}

// splitCommand splits a command line into argv honoring single and double
// quotes (no escapes; quote the whole argument instead).
func splitCommand(s string) []string {
	var argv []string
	var cur strings.Builder
	var quote byte
	inArg := false
	flush := func() {
		if inArg {
			argv = append(argv, cur.String())
			cur.Reset()
			inArg = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
			inArg = true
		case c == ' ' || c == '\t':
			flush()
		default:
			cur.WriteByte(c)
			inArg = true
		}
	}
	flush()
	return argv
}
