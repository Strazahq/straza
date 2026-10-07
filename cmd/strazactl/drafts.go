package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/strazahq/straza/internal/ctl"
)

// draftsLong is the help of the drafts group: what a draft is, the exit
// status every verb speaks, and what runs inside a coding agent.
const draftsLong = "A draft holds App, Role, PolicySet and Removal documents that publish together\n" +
	"or not at all. Straza checks every draft against live state, and nothing in a\n" +
	"draft changes live state until a person publishes it with strazactl drafts publish.\n\n" +
	"Exit status: 0 when the command did its job and, for check, create, update,\n" +
	"revert, rebase and publish, the documents can be published as they stand. 1 when\n" +
	"the server looked and said no: the verdict refuses, the server refused an update,\n" +
	"a revert, a rebase, a contact or a publish, or a contacted server did not answer.\n" +
	"2 when the command could not do its job, for usage, a file, the credential, a\n" +
	"refusal of the caller, the network or the server. list, show and discard never\n" +
	"exit 1.\n\n" +
	"Inside a coding agent on your strazactl login, check, list and show run, and the\n" +
	"verbs that change a draft refuse to run. With STRAZA_API_TOKEN set they run as\n" +
	"that token, and publish then meets the server's refusal, because only a person\n" +
	"publishes."

// draftsCmd is `strazactl drafts`, the verbs of config drafts. Each prints
// the server's own sentences, and exitCode in main.go gives every failure
// that is not the server's no the status 2.
func draftsCmd(client func() *ctl.Client) *cobra.Command {
	drafts := &cobra.Command{
		Use:   "drafts",
		Short: "Check, store, review and publish drafts of changes to MCP servers, roles, access rows and policy sets",
		Long:  draftsLong,
		// The group runs only to refuse a verb it does not have, which exits
		// 2, and to print its help when it is given no verb.
		Args: noVerb,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	drafts.AddCommand(draftsCheckCmd(client), draftsCreateCmd(client), draftsUpdateCmd(client),
		draftsListCmd(client), draftsShowCmd(client), draftsPublishCmd(client),
		draftsDiscardCmd(client), draftsRevertCmd(client), draftsRebaseCmd(client), draftsContactCmd(client))
	return drafts
}

func draftsCheckCmd(client func() *ctl.Client) *cobra.Command {
	var files []string
	var note string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "check -f <path>...",
		Short: "Check documents against live state and print the verdict, storing nothing",
		Long: "Sends the documents to strazad, which checks them against live state and answers\n" +
			"the verdict: what it refuses, what widens access, what does not work yet and what\n" +
			"it could not check. Nothing is stored, so check also runs inside a coding agent\n" +
			"on your login. A file is read as it is, a directory gives its top-level .yaml and\n" +
			".yml files by name, and - reads stdin.",
		Args: noOperands("check"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			docs, names, err := bundleOf(cmd, files, "check", "check -f github.yaml")
			if err != nil {
				return err
			}
			raw, code, err := client().CheckDraft(cmd.Context(), ctl.DraftBody{Documents: docs, Note: note})
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			var a ctl.DraftCheckAnswer
			if err := draftAnswer(w, asJSON, raw, code, 1, &a); err != nil {
				return err
			}
			if !asJSON {
				printItems(w, a.Items)
				fmt.Fprintf(w, "Checked against live state at %s.\n", utcStamp(a.Verdict.CheckedAt))
				printVerdict(w, a.Verdict, placesOf(names, docs))
			}
			yes := "Nothing was stored. The documents can be published: strazactl drafts create" + fileArgs(files)
			if unchanged(a.Items, a.Verdict) {
				yes = nothingToPublish
			}
			return verdictEnd(w, asJSON, a.Verdict, yes,
				"Nothing was stored. The documents cannot be published as they are: fix the refused lines.")
		},
	}
	bundleFlags(cmd, &files, &note, &asJSON)
	return cmd
}

func draftsCreateCmd(client func() *ctl.Client) *cobra.Command {
	var files []string
	var note string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "create -f <path>...",
		Short: "Store the documents as a new draft and print its verdict",
		Args:  noOperands("create"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			docs, names, err := bundleOf(cmd, files, "create", "create -f github.yaml")
			if err != nil {
				return err
			}
			c := client()
			raw, code, err := c.CreateDraft(cmd.Context(), ctl.DraftBody{Documents: docs, Note: note})
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if code == http.StatusUnprocessableEntity && !asJSON {
				return intakeRefused(w, raw, names, docs, "strazactl drafts create"+fileArgs(files))
			}
			var a ctl.DraftAnswer
			if err := draftAnswer(w, asJSON, raw, code, 1, &a); err != nil {
				return err
			}
			if !asJSON {
				fmt.Fprintf(w, "Created draft %s with %s. Nothing changes until a person publishes it.\n",
					a.Draft.ID, plural(len(a.Draft.Items), "document", "documents"))
				printItems(w, a.Draft.Items)
				printVerdict(w, a.Verdict, nil)
			}
			return draftEnd(w, asJSON, a, c.APIToken != "", fileArgs(files))
		},
	}
	bundleFlags(cmd, &files, &note, &asJSON)
	return cmd
}

func draftsUpdateCmd(client func() *ctl.Client) *cobra.Command {
	var files []string
	var note string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "update <id> -f <path>...",
		Short: "Replace an open draft's documents with a new revision and print its verdict",
		Long: "Reads the draft's current revision, then sends the -f documents as the whole new\n" +
			"list of its objects, which the server checks again. --note replaces the note, and\n" +
			"without it the note stays.",
		Args: oneDraft("update"),
		RunE: func(cmd *cobra.Command, args []string) error {
			docs, names, err := bundleOf(cmd, files, "update", "update 41 -f github.yaml")
			if err != nil {
				return err
			}
			c := client()
			if err := c.GuardChange(); err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			raw, code, err := c.GetDraft(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var d ctl.DraftDetail
			if err := draftAnswer(w, false, raw, code, 2, &d); err != nil {
				return err
			}
			body := ctl.DraftUpdate{Revision: d.Draft.Revision, Documents: docs}
			if cmd.Flags().Changed("note") {
				body.Note = &note
			}
			raw, code, err = c.UpdateDraft(cmd.Context(), args[0], body)
			if err != nil {
				return err
			}
			if code == http.StatusUnprocessableEntity && !asJSON {
				return intakeRefused(w, raw, names, docs, "strazactl drafts update "+args[0]+fileArgs(files))
			}
			var a ctl.DraftAnswer
			if err := draftAnswer(w, asJSON, raw, code, 1, &a); err != nil {
				return err
			}
			if !asJSON {
				fmt.Fprintf(w, "Draft %s is at revision %d. Nothing changes until a person publishes it.\n", a.Draft.ID, a.Draft.Revision)
				printItems(w, a.Draft.Items)
				printLeft(w, d.Draft.Items, a.Draft.Items)
				printVerdict(w, a.Verdict, nil)
			}
			return draftEnd(w, asJSON, a, c.APIToken != "", fileArgs(files))
		},
	}
	bundleFlags(cmd, &files, &note, &asJSON)
	return cmd
}

func draftsListCmd(client func() *ctl.Client) *cobra.Command {
	var state string
	var mine, asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List drafts, newest first, the open ones unless --state names others",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, code, err := client().ListDrafts(cmd.Context(), state, mine)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			var page ctl.DraftPage
			if err := draftAnswer(w, asJSON, raw, code, 2, &page); err != nil {
				return err
			}
			if !asJSON {
				fmt.Fprintln(cmd.ErrOrStderr(), listHeader(state, mine))
				if err := printDraftTable(w, page.Items); err != nil {
					return err
				}
				if slices.ContainsFunc(page.Items, func(d ctl.DraftSummary) bool { return checksWord(d) != "-" }) {
					fmt.Fprintln(cmd.ErrOrStderr(), "Counts are from each draft's last check. strazactl drafts show checks it against live state now.")
				}
			}
			if page.NextCursor != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "More drafts exist than these 200. Narrow the list with --state or --mine.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&state, "state", "open", "open, published, discarded, expired or all")
	cmd.Flags().BoolVar(&mine, "mine", false, "only the drafts with a revision you wrote")
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage)
	return cmd
}

func draftsShowCmd(client func() *ctl.Client) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a draft: who proposed it, each object against live state, its verdict and who gains what",
		Args:  oneDraft("show"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client()
			raw, code, err := c.GetDraft(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			var d ctl.DraftDetail
			if err := draftAnswer(w, asJSON, raw, code, 2, &d); err != nil || asJSON {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Draft %s, revision %d, %s: %s\n", d.Draft.ID, d.Draft.Revision, d.Draft.State, d.Draft.Title)
			return printDetail(w, d, c.APIToken != "")
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage)
	return cmd
}

func draftsDiscardCmd(client func() *ctl.Client) *cobra.Command {
	var reason string
	var yes, asJSON bool
	cmd := &cobra.Command{
		Use:   "discard <id>",
		Short: "Discard an open draft, which changes nothing live",
		Args:  oneDraft("discard"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if asJSON && !yes {
				return errJSONNeedsYes("discard", "Pass --yes to discard without the question")
			}
			c := client()
			if err := c.GuardChange(); err != nil {
				return err
			}
			if !yes && !confirm(cmd, "Discard draft "+id+"? Nothing live changes.") {
				return errAborted()
			}
			raw, code, err := c.DiscardDraft(cmd.Context(), id, reason)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			var a struct {
				Draft ctl.Draft `json:"draft"`
			}
			if err := draftAnswer(w, asJSON, raw, code, 2, &a); err != nil || asJSON {
				return err
			}
			fmt.Fprintf(w, "Discarded draft %s.\n", id)
			if a.Draft.Door == "apps-directory" {
				for _, it := range a.Draft.Items {
					if it.Kind == "App" && it.Op == "remove" {
						fmt.Fprintf(w, "%s stays, and its link to the file ends.\n", it.Name)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why, in your own words, at most 500 bytes")
	cmd.Flags().BoolVar(&yes, "yes", false, "discard without the question")
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage)
	return cmd
}

func draftsRevertCmd(client func() *ctl.Client) *cobra.Command {
	var note string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "revert <id>",
		Short: "Make a new draft that undoes a published one, checked like any other",
		Args:  oneDraft("revert"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client()
			raw, code, err := c.RevertDraft(cmd.Context(), args[0], note)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			var a ctl.DraftAnswer
			if err := draftAnswer(w, asJSON, raw, code, 1, &a); err != nil {
				return err
			}
			if !asJSON {
				fmt.Fprintf(w, "Created draft %s, which undoes draft %s. Nothing changes until a person publishes it.\n", a.Draft.ID, args[0])
				printVerdict(w, a.Verdict, nil)
			}
			return draftEnd(w, asJSON, a, c.APIToken != "", " -f <path>")
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "your own words for whoever reviews the undo, at most 2,000 bytes")
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage)
	return cmd
}

// bundleFlags adds -f, --note and --json to a verb that sends documents.
func bundleFlags(cmd *cobra.Command, files *[]string, note *string, asJSON *bool) {
	cmd.Flags().StringArrayVarP(files, "file", "f", nil,
		"App, Role, PolicySet and Removal documents: a YAML file, a directory of .yaml and .yml files, or - for stdin (repeatable)")
	cmd.Flags().StringVar(note, "note", "", "your own words for whoever reviews the draft, at most 2,000 bytes")
	cmd.Flags().BoolVar(asJSON, "json", false, jsonFlagUsage)
}

// noOperands refuses an operand on a verb that reads its documents with -f,
// since the likeliest one is a path given without its -f.
func noOperands(verb string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) == 0 {
			return nil
		}
		return fmt.Errorf("strazactl drafts %s reads documents with -f, one -f for each path, as in strazactl drafts %s -f %s", verb, verb, args[0])
	}
}

// oneDraft refuses a verb that is not given exactly one draft number.
func oneDraft(verb string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) == 1 {
			return nil
		}
		return fmt.Errorf("strazactl drafts %s takes one draft number, such as 41, and got %d. List the drafts with strazactl drafts list", verb, len(args))
	}
}

// errJSONNeedsYes refuses a verb that would ask a question while --json
// keeps stdout for the server's answer. next says what to pass.
func errJSONNeedsYes(verb, next string) error {
	return fmt.Errorf("strazactl drafts %s --json needs --yes, because --json keeps stdout for the server's answer and a question cannot share it. %s", verb, next)
}

// bundleOf reads the documents of the -f paths for verb, whose example
// shows the form when no -f was given, with the name of each text's file.
func bundleOf(cmd *cobra.Command, paths []string, verb, example string) (texts, names []string, err error) {
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("strazactl drafts %s needs its documents with -f: a file, a directory of .yaml and .yml files, or - for stdin, as in strazactl drafts %s", verb, example)
	}
	return readBundle(paths, cmd.InOrStdin())
}

// readBundle reads the -f paths into one text of documents per file, in the
// order given, and leaves the splitting to the server. A directory gives its
// top-level .yaml and .yml files by name, - reads stdin, which can be read
// once, and any other path is read as it is. names holds the file of each
// text, stdin for -.
func readBundle(paths []string, stdin io.Reader) (texts, names []string, err error) {
	stdinRead := false
	for _, p := range paths {
		if p == "-" {
			if stdinRead {
				return nil, nil, errors.New("-f - reads stdin, which can be read once, and it is given twice. Pass -f - once")
			}
			stdinRead = true
			raw, err := io.ReadAll(stdin)
			if err != nil {
				return nil, nil, readError(p, err)
			}
			texts, names = append(texts, string(raw)), append(names, "stdin")
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, nil, readError(p, err)
		}
		files := []string{p}
		if info.IsDir() {
			if files, err = yamlFiles(p); err != nil {
				return nil, nil, err
			}
		}
		for _, f := range files {
			raw, err := os.ReadFile(f) // #nosec G304 -- the operator names the file or its directory
			if err != nil {
				return nil, nil, readError(f, err)
			}
			texts, names = append(texts, string(raw)), append(names, f)
		}
	}
	return texts, names, nil
}

// yamlFiles is the top-level .yaml and .yml files of dir, sorted by name,
// leaving out names that start with a dot, as a shell glob does, so an
// editor's lock or backup file is not sent.
func yamlFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, readError(dir, err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && !strings.HasPrefix(name, ".") && (strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) {
			files = append(files, filepath.Join(dir, name))
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s holds no .yaml or .yml file", dir)
	}
	return files, nil
}

// readError is "cannot read {path}: {error}", without the path a path error
// repeats.
func readError(path string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("cannot read %s: %v", path, err)
}

// fileArgs is the -f paths as a command line spells them, each behind its
// own -f, and quoted when this platform's shell would split or expand it.
func fileArgs(paths []string) string {
	var b strings.Builder
	for _, p := range paths {
		b.WriteString(" -f " + shellWord(p))
	}
	return b.String()
}

// shellWord is s as one word of this platform's shell: cmdWord on Windows,
// where strazactl also ships, and posixWord elsewhere.
func shellWord(s string) string {
	if runtime.GOOS == "windows" {
		return cmdWord(s)
	}
	return posixWord(s)
}

// cmdWord is s as one word that cmd.exe and PowerShell both read: as it is
// unless it is empty or holds a space or a quote, else in double quotes. A
// backslash is a path character there, and a Windows file name cannot hold
// a double quote.
func cmdWord(s string) string {
	if s != "" && !strings.ContainsAny(s, ` '"`) {
		return s
	}
	return `"` + s + `"`
}

// posixWord is s as one POSIX shell word: as it is when every character is
// a letter, a digit or one of _ - . / : @ % + , =, else in single quotes, a
// single quote inside it closing the quotes, escaped, and opening them again.
func posixWord(s string) string {
	special := func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("_-./:@%+,=", r)
	}
	if s != "" && strings.IndexFunc(s, special) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// draftAnswer handles the answer of one drafts call: a status of 400 or
// more through draftFailure, with no as the status of the server's no, and a
// success decoded into out and, under --json, printed whole on w.
func draftAnswer(w io.Writer, asJSON bool, raw []byte, code, no int, out any) error {
	if code >= http.StatusBadRequest {
		return draftFailure(w, asJSON, raw, code, no)
	}
	if asJSON {
		if err := printServerJSON(w, raw); err != nil {
			return err
		}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("strazad's answer is not the JSON a drafts route sends (%v). Check that --server or your login points at strazad", err)
	}
	return nil
}

// draftFailure is the error of a drafts answer of 400 or more. A 409 or 422
// is the server's no and exits with no, 1 for the verbs that end on whether
// a draft can be published and 2 for list, show and discard. Under --json
// its body goes to w, and otherwise its findings or verdict lines do. Any
// other status is a plain error, which main exits with 2.
func draftFailure(w io.Writer, asJSON bool, raw []byte, code, no int) error {
	var r ctl.DraftRefused
	_ = json.Unmarshal(raw, &r)
	sentence := r.Error
	if sentence == "" {
		sentence = noSentence(code)
	}
	if code != http.StatusConflict && code != http.StatusUnprocessableEntity {
		return errors.New(sentence)
	}
	switch {
	case asJSON:
		if err := printServerJSON(w, raw); err != nil {
			return err
		}
	case r.Verdict != nil:
		printVerdict(w, *r.Verdict, nil)
	default:
		printFindings(w, "refused", r.Findings, nil)
	}
	return exitCodeErr{no, errors.New(sentence)}
}

// noSentence words an answer of 400 or more that carries no sentence of
// strazad's, which came from strazad or a proxy in front of it. A 404 or 405
// of a route strazad does not serve never comes here: the client names the
// route.
func noSentence(code int) string {
	return fmt.Sprintf("strazad or a proxy in front of it answered HTTP %d without a sentence. "+
		"Check strazad's log, and the log of any proxy in front of it, then run the command again", code)
}

// verdictEnd ends a verb whose status says whether the documents can be
// published as they stand: yes as the last line and 0, or no and 1. Under
// --json the line is not printed, and no becomes the error on stderr.
func verdictEnd(w io.Writer, asJSON bool, v ctl.DraftVerdict, yes, no string) error {
	switch {
	case len(v.Refused) == 0:
		if !asJSON {
			fmt.Fprintln(w, yes)
		}
		return nil
	case asJSON:
		return exitCodeErr{1, errors.New(no)}
	}
	fmt.Fprintln(w, no)
	return exitCodeErr{code: 1}
}

// publishHint is the last line of a draft that can be published.
func publishHint(id string) string {
	return "The draft can be published: strazactl drafts publish " + id
}

// updateHint is the last line of a draft its check refuses, with the -f
// arguments that send its fixed documents.
func updateHint(id, files string) string {
	return "The draft cannot be published yet. Fix it and send it again: strazactl drafts update " + id + files
}

// listHeader is the stderr line above the drafts table.
func listHeader(state string, mine bool) string {
	switch {
	case state == "all" && mine:
		return "All your drafts, newest first."
	case state == "all":
		return "All drafts, newest first."
	case mine:
		return "Your " + state + " drafts, newest first."
	case state == "":
		return "Drafts, newest first."
	}
	return strings.ToUpper(state[:1]) + state[1:] + " drafts, newest first."
}
