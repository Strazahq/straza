package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/strazahq/straza/internal/ctl"
	"github.com/strazahq/straza/internal/drafts"
)

// nothingToPublish is the last line of a check, a create or an update whose
// every document equals live.
const nothingToPublish = "Nothing to publish: every document equals live."

// draftEnd ends create, update, revert and rebase on the draft a answered,
// as verdictEnd does. A draft whose every document equals live has nothing
// to publish. When token says the caller is an admin API token, which never
// publishes, a draft that can be published points to a person's login. A
// stale draft points to Check again, because no update clears it. files is
// the -f arguments that send its fixed documents.
func draftEnd(w io.Writer, asJSON bool, a ctl.DraftAnswer, token bool, files string) error {
	yes, no := publishHint(a.Draft.ID), updateHint(a.Draft.ID, files)
	switch {
	case unchanged(a.Draft.Items, a.Verdict):
		yes = nothingToPublish
	case token:
		yes = tokenNext(a.Draft.ID) + "."
	}
	if stale(a.Verdict) {
		no = staleHint(a.Draft.ID)
	}
	return verdictEnd(w, asJSON, a.Verdict, yes, no)
}

// unchanged reports whether the verdict v finds every one of items equal to
// live.
func unchanged(items []ctl.DraftItem, v ctl.DraftVerdict) bool {
	same := map[string]bool{}
	for _, f := range v.Info {
		if f.Code == "info.no-change" {
			same[f.Object] = true
		}
	}
	return len(items) > 0 && !slices.ContainsFunc(items, func(it ctl.DraftItem) bool { return !same[it.Object()] })
}

// stale reports whether the verdict v refuses an object that changed after
// the draft was checked. A revision keeps the base of every object it
// holds, so no update of the draft clears the refusal.
func stale(v ctl.DraftVerdict) bool {
	return slices.ContainsFunc(v.Refused, func(f ctl.DraftFinding) bool { return f.Code == "draft.stale" })
}

// staleHint is the last line of the stale draft id: Check again, which
// keeps what the draft changed and moves its base to live state.
func staleHint(id string) string {
	return "Check the draft again, which keeps what it changed and takes every other field from live state: strazactl drafts rebase " + id
}

// tokenNext is the way from an admin API token to the publish of draft id:
// a token never publishes, and strazactl login refuses to run while one is
// set.
func tokenNext(id string) string {
	return "Unset STRAZA_API_TOKEN, run strazactl login, then strazactl drafts publish " + id
}

// intakeRefused prints the 422 of a create or an update that intake
// refused, where nothing was stored: the refused lines, under each one the
// bundle reader answered where its document or text sits, and then again,
// the command to run once the files are fixed. It exits 1 with nothing on
// stderr, as a check that refuses does. A refusal of the note or of the
// proposer alone is no fault of the files, and then the end names no
// command. A body without findings is printed as any other refusal is.
func intakeRefused(w io.Writer, raw []byte, names, texts []string, again string) error {
	var r ctl.DraftRefused
	if json.Unmarshal(raw, &r) != nil || len(r.Findings) == 0 {
		return draftFailure(w, false, raw, http.StatusUnprocessableEntity, 1)
	}
	printFindings(w, "refused", r.Findings, placesOf(names, texts))
	end := "Nothing was stored."
	if slices.ContainsFunc(r.Findings, aboutFiles) {
		end += " Fix the files and run " + again + " again."
	}
	fmt.Fprintln(w, end)
	return exitCodeErr{code: 1}
}

// aboutFiles reports whether a refusal at intake is about the documents and
// not about the note, the proposer's open drafts or an agent's sponsor.
func aboutFiles(f ctl.DraftFinding) bool {
	return f.Object != "Note" && f.Code != "draft.open-limit" && f.Code != "agent.sponsor"
}

// placesOf maps the number of each document of texts, the number a
// refusal of reading it carries in document, to where that document sits,
// as the line under the refusal says. names holds the file of each text,
// stdin for -. The server numbers documents across every text it is sent,
// and strazactl sends one text per file, so the bundle reader run here on
// the same texts numbers them alike.
func placesOf(names, texts []string) map[int]string {
	out := map[int]string{}
	for n, p := range drafts.Places(texts) {
		if p.Text < len(names) {
			out[n] = placeWords(p, names[p.Text], len(texts) > 1)
		}
	}
	return out
}

// placeWords says where p sits in the text of the file name, stdin for -:
// the file alone for a whole text or when several is false, since one text
// numbers its documents as the server does, and otherwise the document's
// place in its file, then its kind and name when they read.
func placeWords(p drafts.Place, name string, several bool) string {
	where := "the " + ordinal(p.Doc) + " document of " + name
	switch {
	case p.Doc == 0 || !several:
		return name
	case name == "stdin":
		where = "the " + ordinal(p.Doc) + " document read from stdin"
	}
	switch {
	case p.Kind != "" && p.Name != "":
		return where + ", " + article(p.Kind) + " " + p.Kind + " named " + p.Name
	case p.Kind != "":
		return where + ", " + article(p.Kind) + " " + p.Kind
	}
	return where
}

// ordinal is n as an ordinal: first to tenth in words, then 11th, 21st and
// on.
func ordinal(n int) string {
	if n >= 1 && n <= 10 {
		return [...]string{"first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth", "ninth", "tenth"}[n-1]
	}
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return strconv.Itoa(n) + suffix
}

// article is the indefinite article of word: an before a vowel, else a.
func article(word string) string {
	if strings.ContainsRune("AEIOUaeiou", []rune(word)[0]) {
		return "an"
	}
	return "a"
}
