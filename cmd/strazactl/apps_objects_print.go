package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/strazahq/straza/internal/ctl"
)

// printObjects prints one page of a server's object list: the status lines,
// the drift line, the table and the command for the next page. A server
// that pins no type prints what its marks declare instead. after and limit
// are the page the operator asked for, and now dates the ages.
func printObjects(w io.Writer, l ctl.ObjectList, after string, limit int, now time.Time) error {
	if l.Type == "" {
		return printUnpinned(w, l)
	}
	for _, line := range objectsStatus(l, now) {
		fmt.Fprintln(w, line)
	}
	if l.Drift {
		fmt.Fprintf(w, "%s's marks differ from the pinned %s. Draft the change with strazactl apps objects draft %s.\n", l.App, l.Plural, l.App)
	}
	if len(l.Objects) == 0 {
		switch {
		case after != "":
			fmt.Fprintf(w, "No %s after %s.\n", l.Plural, plainText(after))
		case l.Listing != nil && l.Listing.Outcome == "ok":
			fmt.Fprintf(w, "%s's list holds no %s.\n", l.App, l.Plural)
		}
		return nil
	}
	if err := writeObjectsTable(w, l); err != nil {
		return err
	}
	if l.Next != "" {
		next := "strazactl apps objects " + l.App + " --after " + posixWord(plainText(l.Next))
		if limit > 0 {
			next += fmt.Sprintf(" --limit %d", limit)
		}
		fmt.Fprintf(w, "More %s follow. Next page: %s\n", l.Plural, next)
	}
	return nil
}

// objectsStatus is the status of the server's listing in sentences: the
// listing tool, then the last listing with its count and change, the reason
// of a failed listing and the list it keeps, or that it has not been listed
// yet and, when it has no listing credential, how to add one.
func objectsStatus(l ctl.ObjectList, now time.Time) []string {
	s := l.Listing
	if s == nil {
		return []string{fmt.Sprintf("%s pins its %s, and Straza has not listed them yet.", l.App, l.Plural)}
	}
	head := fmt.Sprintf("%s lists its %s with %s.", l.App, l.Plural, s.Tool)
	count := plural(s.Count, l.Type, l.Plural)
	switch s.Outcome {
	case "ok":
		return []string{fmt.Sprintf("%s Last listing %s: %s%s.", head, ageOf(s.ListedAt, now), count, changeWords(*s))}
	case "failed":
		out := []string{head, plainText(s.Reason)}
		if s.ListedAt != "" {
			out = append(out, fmt.Sprintf("The last good list stays: %s, listed %s.", count, ageOf(s.ListedAt, now)))
		}
		return out
	}
	out := []string{head + " It has not been listed yet."}
	if s.Credential == "missing" {
		out = append(out, fmt.Sprintf("%[1]s has no listing credential, so Straza cannot list its %[2]s. "+
			"Add one with strazactl apps secret set %[1]s --listing, then refresh with strazactl apps objects refresh %[1]s.", l.App, l.Plural))
	}
	return out
}

// changeWords is what the last change of the list did, after the count:
// nothing on the first version, which every entry joined, and otherwise the
// entries that joined and left, or no change.
func changeWords(s ctl.ObjectListing) string {
	if s.Version <= 1 {
		return ""
	}
	var out string
	if s.Joined > 0 {
		out += fmt.Sprintf(", %d new", s.Joined)
	}
	if s.Left > 0 {
		out += fmt.Sprintf(", %d gone", s.Left)
	}
	if out == "" {
		return ", no change"
	}
	return out
}

// ageOf is the age of the RFC 3339 time ts at now in words, or the time as
// strazad sent it when it does not parse.
func ageOf(ts string, now time.Time) string {
	at, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "at " + plainText(ts)
	}
	return ageWords(now.Sub(at))
}

// ageWords is a duration as a person reads an age: less than a minute,
// minutes, hours up to two days, then days.
func ageWords(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute ago"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute", "minutes") + " ago"
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "hour", "hours") + " ago"
	}
	return plural(int(d/(24*time.Hour)), "day", "days") + " ago"
}

// writeObjectsTable prints the entries: value, id, title, one column per
// attribute the manifest keeps, and the roles that cover each.
func writeObjectsTable(w io.Writer, l ctl.ObjectList) error {
	head := []string{"VALUE", "ID", "TITLE"}
	for _, a := range l.Attributes {
		head = append(head, strings.ToUpper(plainText(a.Name)))
	}
	head = append(head, "COVERED BY")
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(head, "\t"))
	for _, o := range l.Objects {
		row := []string{plainText(o.Value), dashIfEmpty(plainText(o.ID)), dashIfEmpty(plainText(o.Title))}
		for _, a := range l.Attributes {
			row = append(row, dashIfEmpty(plainText(strings.Join(o.Attributes[a.Name], ", "))))
		}
		covered := "no role"
		if len(o.CoveredBy) > 0 {
			covered = strings.Join(o.CoveredBy, ", ")
		}
		fmt.Fprintln(tw, strings.Join(append(row, covered), "\t"))
	}
	return tw.Flush()
}

// plainText is text an upstream server wrote with every control character,
// a tab, a line break or a terminal escape among them, turned into a space,
// so it can neither break the table nor drive the operator's terminal.
func plainText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// dashIfEmpty is s, or "-" when it is empty.
func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// printUnpinned prints what a server that pins no type declares in its tool
// marks, as the straza.objects block a manifest would hold, then its faults
// and the fix, or the command that opens a draft to pin it.
func printUnpinned(w io.Writer, l ctl.ObjectList) error {
	if raw := bytes.TrimSpace(l.Declared); len(raw) == 0 || string(raw) == "null" {
		fmt.Fprintf(w, "%s pins no object type and none of its tools carries an ai.straza/object mark. "+
			"Write straza.objects in its manifest by hand, or ask the server's author to mark its tools.\n", l.App)
		return nil
	}
	block, err := declaredYAML(l.Declared)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%s pins no object type. Its tool marks declare this block:\n", l.App)
	if _, err := w.Write(block); err != nil {
		return err
	}
	for _, f := range l.DeclaredFaults {
		fmt.Fprintln(w, plainText(f))
	}
	if len(l.DeclaredFaults) > 0 {
		fmt.Fprintln(w, "Fix the marks on the server, or write straza.objects in its manifest by hand.")
		return nil
	}
	fmt.Fprintf(w, "Pin it with strazactl apps objects draft %s, then publish the draft.\n", l.App)
	return nil
}

// declaredYAML is the declared block under straza.objects as block-style
// YAML, in the order strazad sent its keys. JSON is YAML, so the block is
// read as a YAML node, which keeps every key a later revision adds, and its
// flow style is cleared.
func declaredYAML(raw json.RawMessage) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, fmt.Errorf("strazad's declared block is not the JSON object the objects route sends. " +
			"Check that --server or STRAZA_SERVER points at strazad, or run the command with --json")
	}
	body := doc.Content[0]
	blockStyle(body)
	key := func(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: s} }
	top := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		key("straza"), {Kind: yaml.MappingNode, Content: []*yaml.Node{key("objects"), body}},
	}}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(top); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// blockStyle clears the flow and quoting style of n and every node under
// it, so the encoder writes block style and quotes only where YAML needs it.
func blockStyle(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		blockStyle(c)
	}
}
