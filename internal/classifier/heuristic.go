// Package classifier ships the heuristic backend behind the
// policy.Classifier seam: interpreter-aware single-event analysis with no
// model, no I/O and no session state.
//
// Philosophy: deny ONLY on a clear indirection signal, naming the signal and
// the offending fragment. When in doubt, allow. The policy layer decides
// which events reach the classifier at all, and the async sentinel lane
// catches cross-event patterns, so a single-event miss here is not the last
// line of defence, but a single-event false positive would be a self-inflicted
// DoS. Every check below is tuned to that asymmetry.
package classifier

import (
	"context"

	"github.com/strazahq/straza/internal/policy"
)

// maxNest bounds recursion into an inline-eval payload or the inner command of
// a substitution: one level, matching the design rule
// "recurse ONE level into the payload" (a payload that itself wraps an
// interpreter, e.g. `bash -c "python3 -c '…'"`). Deeper nesting is rare and
// re-parsing it faithfully is unreliable; we stop rather than guess.
const maxNest = 1

// NewHeuristic returns the no-model classifier both PEPs embed.
func NewHeuristic() policy.Classifier { return &heuristic{} }

type heuristic struct{}

// Classify judges ONE event. It fails closed on a cancelled context (callers
// treat any error as deny), only ever inspects shell.exec (every other tool
// passes through allow; indirection is a shell concern), and analyses the
// command/argv directly rather than trusting ev.Interpreter, since server-side
// callers may hand us untagged events.
func (h *heuristic) Classify(ctx context.Context, ev policy.Event) (policy.ClassifyVerdict, error) {
	if err := ctx.Err(); err != nil {
		return policy.ClassifyVerdict{}, err // cancelled/deadline ⇒ error ⇒ caller denies
	}
	if ev.Tool != policy.ToolShellExec {
		return policy.ClassifyVerdict{Allowed: true}, nil
	}
	if reason, deny := h.analyze(ev.Command, ev.Argv, 0); deny {
		return policy.ClassifyVerdict{Allowed: false, Reason: reason}, nil
	}
	return policy.ClassifyVerdict{Allowed: true}, nil
}

// analyze runs the signal battery over one command string (with optional
// pre-split argv). It is the recursion point: an inline-eval payload and the
// inner command of each substitution are fed back through analyze at depth+1
// so nested pipe-to-shell / encoded / eval
// shapes are seen structurally, not just as substrings.
func (h *heuristic) analyze(cmd string, argv []string, depth int) (string, bool) {
	// Pipeline shapes (pipe-to-shell, decode-then-exec) need the raw string
	// with its operators intact; argv is a fallback when Command is empty.
	pcmd := cmd
	if pcmd == "" {
		pcmd = joinArgv(argv)
	}
	if reason, deny := pipelineSignal(pcmd); deny {
		return reason, true
	}
	// eval, source and . are no interpreter family, and neither is a command
	// such as echo that holds a substitution, so the substitution checks run
	// here, before the no-family return below.
	cmds := simpleCommands(pcmd)
	if reason, deny := substSignal(cmds); deny {
		return reason, true
	}
	if depth < maxNest {
		for _, c := range cmds {
			var code map[string]bool // built once per command, on first need
			for _, s := range c.substs {
				inner := s.inner
				// cat reading a quoted heredoc yields the body as text. That
				// text is commands only where it lands in a code position, and
				// there it is the body, not cat, that runs.
				if body, ok := catBody(inner); ok {
					if code == nil {
						code = map[string]bool{}
						for _, in := range codeSubsts(c) {
							code[in] = true
						}
					}
					if !code[inner] {
						continue
					}
					inner = body
				}
				if reason, deny := h.analyze(inner, nil, depth+1); deny {
					return "nested-eval: " + reason, true
				}
			}
		}
	}
	// eval runs its arguments as a shell program, the same as bash -c. It is
	// read in addition to the interpreter path below, never in its place.
	if payload, ok := evalProgram(cmds); ok {
		if reason, deny := scanPayloadSignals(payload); deny {
			return reason, true
		}
		if depth < maxNest {
			if reason, deny := h.analyze(payload, nil, depth+1); deny {
				return "nested-eval: " + reason, true
			}
		}
	}

	// Inline-eval: an interpreter carrying its program on the command line.
	words := argv
	if len(words) == 0 {
		words = splitWords(cmd)
	}
	base, idx := leadBase(words)
	fam := interpFamily(base)
	if fam == "" {
		return "", false
	}
	if fam == famPwsh && hasEncodedFlag(words, idx) {
		// -EncodedCommand is base64 by construction: nothing legitimate needs
		// its argv hidden from policy. Deny without decoding it.
		return "encoded-command: obfuscated PowerShell -EncodedCommand", true
	}
	payload, ok := evalPayload(fam, words, idx)
	if !ok {
		return "", false // interpreter running a file/REPL, not an inline program
	}
	if reason, deny := scanPayloadSignals(payload); deny {
		return reason, true
	}
	if depth < maxNest {
		if reason, deny := h.analyze(payload, nil, depth+1); deny {
			return "nested-eval: " + reason, true
		}
	}
	return "", false
}
