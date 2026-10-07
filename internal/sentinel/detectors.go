package sentinel

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/redact"
)

// The detectors of the audit sentinel: rule engines over
// sliding windows, no model. Each returns zero or one verdict;
// per-session dedup keeps the alert lane from becoming its own flood.

// variantOverlap is the normalized token-set overlap at which a follow-up
// command counts as a variant of a denied one.
const variantOverlap = 0.6

// denyBurst: an agent probing the policy edge, N denials inside
// DenyBurstWindow. Warn at the low threshold, critical at the high one;
// each severity dedups independently so an escalating burst still surfaces
// the escalation.
func (e *engine) denyBurst(s *session, ev event) []Verdict {
	if ev.Effect != policy.EffectDeny {
		return nil
	}
	w := e.cfg.DenyBurstWindow
	s.denies = append(s.denies, evRef{at: ev.At, id: ev.ID})
	if n := len(s.denies); n > maxDenies {
		s.denies = append(s.denies[:0], s.denies[n-maxDenies:]...)
	}
	s.denies = pruneRefs(s.denies, ev.At.Add(-w))
	n := len(s.denies)
	var sev string
	switch {
	case n >= e.cfg.DenyBurstCritical:
		sev = SeverityCritical
	case n >= e.cfg.DenyBurstWarn:
		sev = SeverityWarn
	default:
		return nil
	}
	if !s.dedup(DetectorDenyBurst+"/"+sev, ev.At, w) {
		return nil
	}
	ids := make([]string, len(s.denies))
	for i, r := range s.denies {
		ids[i] = r.id
	}
	return []Verdict{{
		Session: ev.Session, User: s.user,
		Detector: DetectorDenyBurst, Severity: sev,
		Reason:   fmt.Sprintf("%d denied actions within %s. The agent is probing the policy edge", n, w),
		Evidence: ids, Window: w.String(),
	}}
}

// denyThenVariant: a denied shell command re-attempted mutated: token-set
// overlap >= 60% with the denied command (which, because tokenization strips
// quotes, also catches `bash -c '...'` / `python -c '...'` inline-eval
// wraps), or the denied command reappearing base64-encoded. An identical
// retry is NOT a variant (denyBurst owns plain retries).
func (e *engine) denyThenVariant(s *session, ev event) []Verdict {
	if ev.Tool != policy.ToolShellExec || strings.TrimSpace(ev.Command) == "" {
		return nil
	}
	w := e.cfg.VariantWindow
	s.denied = pruneDenied(s.denied, ev.At.Add(-w))
	toks := tokenize(ev.Command)
	var out []Verdict
	for i := range s.denied {
		d := &s.denied[i]
		var kind string
		switch {
		case d.cmd != ev.Command && overlap(d.tokens, toks) >= variantOverlap:
			kind = "rewritten/wrapped"
		case d.b64 != "" && strings.Contains(ev.Command, d.b64):
			kind = "base64-encoded"
		default:
			continue
		}
		if s.dedup(DetectorDenyVariant, ev.At, w) {
			out = append(out, Verdict{
				Session: ev.Session, User: s.user,
				Detector: DetectorDenyVariant, Severity: SeverityCritical,
				Reason:   fmt.Sprintf("a denied command was re-attempted as a %s variant within %s", kind, w),
				Evidence: []string{d.id, ev.ID}, Window: w.String(),
			})
		}
		break
	}
	if ev.Effect == policy.EffectDeny {
		rec := deniedCmd{at: ev.At, id: ev.ID, cmd: ev.Command, tokens: toks}
		if len(ev.Command) >= minB64CmdLen {
			// RawStdEncoding is a prefix of the padded form, so one substring
			// probe covers both padded and unpadded re-encodings.
			rec.b64 = base64.RawStdEncoding.EncodeToString([]byte(ev.Command))
		}
		s.denied = append(s.denied, rec)
		if n := len(s.denied); n > maxDeniedCmds {
			s.denied = append(s.denied[:0], s.denied[n-maxDeniedCmds:]...)
		}
	}
	return out
}

// writeThenExecute: a file.write followed by a shell.exec referencing one of
// the written paths, the interpreter-indirection sequence itself, which the
// single-event classifier cannot see. Only non-denied writes are tracked: a
// denied write wrote nothing.
func (e *engine) writeThenExecute(s *session, ev event) []Verdict {
	w := e.cfg.WriteExecWindow
	switch ev.Tool {
	case policy.ToolFileWrite:
		if ev.Effect == policy.EffectDeny {
			return nil
		}
		for _, p := range ev.Paths {
			if len(p) < minPathLen {
				continue
			}
			s.writes = append(s.writes, writeRec{at: ev.At, id: ev.ID, path: p})
		}
		if n := len(s.writes); n > maxWrites {
			s.writes = append(s.writes[:0], s.writes[n-maxWrites:]...)
		}
	case policy.ToolShellExec:
		if ev.Command == "" {
			return nil
		}
		s.writes = pruneWrites(s.writes, ev.At.Add(-w))
		for _, wr := range s.writes {
			if !refsPath(ev.Command, wr.path) {
				continue
			}
			if !s.dedup(DetectorWriteExec, ev.At, w) {
				return nil
			}
			return []Verdict{{
				Session: ev.Session, User: s.user,
				Detector: DetectorWriteExec, Severity: SeverityCritical,
				Reason:   fmt.Sprintf("the session wrote %s and then executed it: interpreter indirection", wr.path),
				Evidence: []string{wr.id, ev.ID}, Window: w.String(),
			}}
		}
	}
	return nil
}

// pemPrivateKeyRe is the leak DETECTOR's PEM matcher: it fires on a bare
// private-key header even with no END delimiter (a leaked header is a leak).
// This is deliberately NOT the shared redact.PEMBlock, which masks a COMPLETE
// block; masking and detection are different jobs, so this one stays local.
var pemPrivateKeyRe = regexp.MustCompile(`-----BEGIN (?:[A-Z]+ )*PRIVATE KEY-----`)

// leakPatterns reuse the shared credential battery (internal/redact) for the
// four shapes it defines, keeping the local header-only PEM detector. Order and
// labels are fixed so the emitted verdict text stays byte-stable.
// Conservative shapes only: a false credential alarm burns trust fast.
var leakPatterns = []struct {
	label string
	re    *regexp.Regexp
}{
	{"AWS access key id", redact.AWSKey},
	{"PEM private key", pemPrivateKeyRe},
	{"GitHub token", redact.GitHubToken},
	{"bearer token", redact.BearerToken},
	{"Straza token", redact.StrazaToken},
}

// captureContent: credential shapes inside captured prompt/reply text.
// Privacy is STRUCTURAL here: prompt/reply CEs exist only for roles whose
// capture policy opted in (spec/policyset capture directive); the sentinel
// reads what capture already emits and widens surveillance for no one.
// Content captured in redact mode arrives pre-masked and simply won't match.
func (e *engine) captureContent(s *session, ev event) []Verdict {
	if ev.Content == "" {
		return nil
	}
	var labels []string
	for _, p := range leakPatterns {
		if p.re.MatchString(ev.Content) {
			labels = append(labels, p.label)
		}
	}
	if len(labels) == 0 || !s.dedup(DetectorCaptureContent, ev.At, dedupWindow) {
		return nil
	}
	return []Verdict{{
		Session: ev.Session, User: s.user,
		Detector: DetectorCaptureContent, Severity: SeverityCritical,
		Reason:   "credential pattern in captured " + ev.Kind + " content: " + strings.Join(labels, ", "),
		Evidence: []string{ev.ID},
	}}
}

// toolMix: a session using a tool outside the user's established baseline.
// Coarse counts first. Baselines live since boot and are keyed by USER
// because the audit payload has no role field.
func (e *engine) toolMix(s *session, ev event) []Verdict {
	if ev.User == "" {
		return nil
	}
	key := ev.Tool
	if ev.Kind == "mcp" && ev.App != "" && ev.ToolName != "" {
		key = "mcp:" + ev.App + "/" + ev.ToolName
	}
	if key == "" {
		return nil
	}
	b := e.users.getOrPut(ev.User, e.maxUsers, func() *baseline {
		return &baseline{tools: map[string]bool{}}
	})
	b.lastSeen = e.now()
	established := b.total >= e.cfg.BaselineMinEvents
	novel := !b.tools[key]
	if novel && len(b.tools) < maxBaselineTools {
		b.tools[key] = true // saturated baselines stop growing (bounded memory)
	}
	b.total++
	if !established || !novel || !s.dedup(DetectorToolMix, ev.At, dedupWindow) {
		return nil
	}
	return []Verdict{{
		Session: ev.Session, User: ev.User,
		Detector: DetectorToolMix, Severity: SeverityInfo,
		Reason:   fmt.Sprintf("tool %s is outside this user's established mix (%d prior events since boot)", key, b.total-1),
		Evidence: []string{ev.ID},
	}}
}

// --- shared helpers ---

func pruneRefs(in []evRef, cut time.Time) []evRef {
	out := in[:0]
	for _, r := range in {
		if r.at.After(cut) {
			out = append(out, r)
		}
	}
	return out
}

func pruneDenied(in []deniedCmd, cut time.Time) []deniedCmd {
	out := in[:0]
	for _, d := range in {
		if d.at.After(cut) {
			out = append(out, d)
		}
	}
	return out
}

func pruneWrites(in []writeRec, cut time.Time) []writeRec {
	out := in[:0]
	for _, w := range in {
		if w.at.After(cut) {
			out = append(out, w)
		}
	}
	return out
}

// splitCmd splits a shell command on whitespace, shell metacharacters, and
// quotes (case preserved; paths need it).
func splitCmd(cmd string) []string {
	return strings.FieldsFunc(cmd, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', ';', '|', '&', '(', ')', '<', '>', '"', '\'', '`':
			return true
		}
		return false
	})
}

// tokenize builds the normalized (lowercased) token set for similarity.
func tokenize(cmd string) map[string]bool {
	set := make(map[string]bool)
	for _, f := range splitCmd(cmd) {
		set[strings.ToLower(f)] = true
	}
	return set
}

// overlap is the fraction of the DENIED command's tokens present in the
// candidate. The denominator is the denied set on purpose: wrappers add
// tokens (`bash -c ...`), and a Jaccard measure would let padding dilute the
// match below threshold.
func overlap(denied, candidate map[string]bool) float64 {
	if len(denied) == 0 {
		return 0
	}
	hit := 0
	for t := range denied {
		if candidate[t] {
			hit++
		}
	}
	return float64(hit) / float64(len(denied))
}

// refsPath reports whether a command references a written path: the full
// path as a substring, or the path's basename as a whole token (covering
// `./script.py` and bare-basename invocations without matching arbitrary
// substrings like "data.sh" for "a.sh").
func refsPath(cmd, path string) bool {
	if strings.Contains(cmd, path) {
		return true
	}
	base := path[strings.LastIndexAny(path, `/\`)+1:]
	if len(base) < minPathLen {
		return false
	}
	for _, tok := range splitCmd(cmd) {
		if tok == base || strings.HasSuffix(tok, "/"+base) || strings.HasSuffix(tok, `\`+base) {
			return true
		}
	}
	return false
}
