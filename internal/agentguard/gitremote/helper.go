// Package gitremote is git-remote-straza, the git remote helper that takes
// an agent's git command to `straza service` over its socket or pipe. The
// service asks the Straza server for a git grant and relays git's smart
// HTTP to the resource server that holds the git host's key, so no git
// credential sits on the machine. A repo no Straza server lists goes to
// git's own https transport untouched.
package gitremote

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/strazahq/straza/internal/agentguard"
	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// Name is the program name git runs for a straza:: remote. The straza
// binary answers to it through a symlink or a copy with that name.
const Name = "git-remote-straza"

// harnessVersion is the harness version the helper names in the window
// headers, as the MCP bridge names "mcp". The service uses it only when it
// starts a session for a caller that named no window.
const harnessVersion = "git"

// Sentences the helper prints on stderr, where git shows them as they are.
const (
	sentArgs = "Straza: git-remote-straza needs two arguments, the remote and its URL, and git gave it %d, so this " +
		"git command did not run. Use a remote whose URL starts with straza::https://."
	sentUnknownCommand = "Straza: git asked git-remote-straza for %q, which it does not support, so this git command " +
		"did not run. Update Straza on this machine, then run git again."
	sentUnknownService = "Straza: git asked git-remote-straza to connect to %s, which Straza does not relay, so this " +
		"git command did not run. Run git push, git fetch or git clone instead."
	sentArchive = "Straza: git archive --remote does not go through Straza. Clone the repo and run git archive locally."
	sentGitRead = "Straza: git-remote-straza could not read what git sent (%v), so this git command did not finish. " +
		"Run git again."
	sentHandoff = "Straza: no Straza server serves %s, so git-remote-straza handed it to git's own https transport, " +
		"but git could not start (%v). Check that git is on PATH, then run git again."
)

// Invoked reports whether argv0, the program name the system started this
// process with, names the helper.
func Invoked(argv0 string) bool {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(argv0)), ".exe") == Name
}

// Main runs the helper with git's arguments, the remote and its URL, and
// returns the exit code. It reads git's commands on stdin and answers on
// stdout, and every sentence for the person goes to stderr. When no Straza
// server lists the repo it hands the run to `git remote-https`, by exec on
// Unix and as a child process elsewhere.
func Main(args []string) int {
	h, err := newHelper(args, environ(), os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		h.say(err)
		return 1
	}
	h.base, h.http = localapi.BaseURL, agentguard.NewServiceClient().HTTPClient()
	code, handOff := h.run(context.Background())
	if handOff {
		return handoff(h.remote, h.url, h.errw)
	}
	return code
}

// helper is one run of git-remote-straza.
type helper struct {
	remote, url string
	op          string
	window      localapi.Harness
	base        string
	http        *http.Client
	in          *bufio.Reader
	out         io.Writer
	errw        io.Writer
}

// newHelper checks git's arguments and reads the harness window from env.
// It never reads stdin, so a hand-off to git's own transport finds it as
// git left it.
func newHelper(args []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) (*helper, error) {
	h := &helper{in: bufio.NewReaderSize(stdin, textMax), out: stdout, errw: stderr, window: windowOf(env)}
	if len(args) != 2 {
		return h, stopf(sentArgs, len(args))
	}
	h.remote, h.url = args[0], args[1]
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return h, stopf("Straza: git-remote-straza could not read random bytes from the system (%v), so this git "+
			"command did not run. Run git again.", err)
	}
	h.op = hex.EncodeToString(id)
	return h, nil
}

// windowOf names the harness window the helper runs in. The session comes
// from CLAUDE_CODE_SESSION_ID, then CODEX_THREAD_ID, and the harness name
// from the markers the hook reads (agentguard.DetectHarness), then from the
// variable that named the session, then claude-code, as the MCP bridge
// picks it. With no session the service uses the caller's newest live
// session of that harness.
func windowOf(env map[string]string) localapi.Harness {
	w := localapi.Harness{Version: harnessVersion}
	fallback := "claude-code"
	switch {
	case env["CLAUDE_CODE_SESSION_ID"] != "":
		w.Session = env["CLAUDE_CODE_SESSION_ID"]
	case env["CODEX_THREAD_ID"] != "":
		w.Session, fallback = env["CODEX_THREAD_ID"], "codex"
	}
	w.Name = agentguard.DetectHarness("", env, nil, nil)
	if w.Name == "" {
		w.Name = fallback
	}
	return w
}

// environ returns the process environment as a map.
func environ() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

// stop is an error that carries the sentence the person reads.
type stop struct{ sentence string }

func (s *stop) Error() string { return s.sentence }

func stopf(format string, a ...any) error { return &stop{fmt.Sprintf(format, a...)} }

// say prints the sentence of err on stderr.
func (h *helper) say(err error) {
	var s *stop
	if !errors.As(err, &s) {
		s = &stop{fmt.Sprintf(sentGitRead, err)}
	}
	_, _ = fmt.Fprintln(h.errw, s.sentence)
}

// run serves one helper run. It asks the service first, before it reads
// stdin, whether a Straza server lists the repo, and reports a hand-off
// when none does.
func (h *helper) run(ctx context.Context) (code int, handOff bool) {
	listed, err := h.resolve(ctx)
	if err != nil {
		h.say(err)
		return 1, false
	}
	if !listed {
		return 0, true
	}
	if err := h.serve(ctx); err != nil {
		h.say(err)
		return 1, false
	}
	return 0, false
}

// serve answers git's helper commands until git ends them or a connect
// takes over the streams. The helper offers only connect, so git sends
// capabilities, perhaps options, and then one connect.
func (h *helper) serve(ctx context.Context) error {
	for {
		line, err := h.in.ReadSlice('\n')
		cmd := strings.TrimSuffix(string(line), "\n")
		switch {
		case errors.Is(err, io.EOF) && cmd == "":
			return nil
		case err != nil:
			return stopf(sentGitRead, err)
		}
		switch {
		case cmd == "capabilities":
			err = h.write([]byte("connect\n\n"))
		case strings.HasPrefix(cmd, "option "):
			err = h.write([]byte("unsupported\n"))
		case cmd == "":
			return nil
		case strings.HasPrefix(cmd, "connect "):
			return h.connect(ctx, strings.TrimPrefix(cmd, "connect "))
		default:
			return stopf(sentUnknownCommand, clip(cmd))
		}
		if err != nil {
			return err
		}
	}
}

// connect relays one git service.
func (h *helper) connect(ctx context.Context, service string) error {
	switch service {
	case "git-upload-pack":
		return h.fetch(ctx)
	case "git-receive-pack":
		return h.push(ctx)
	case "git-upload-archive":
		return &stop{sentArchive}
	}
	return stopf(sentUnknownService, clip(service))
}

// write sends b to git.
func (h *helper) write(b []byte) error {
	if _, err := h.out.Write(b); err != nil {
		return stopf("Straza: git-remote-straza could not write to git (%v), so this git command did not finish. "+
			"Run git again.", err)
	}
	return nil
}
