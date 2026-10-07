package server

import (
	"fmt"
	"strings"
)

// readMiss is a grant the caller lacks to read a draft's items, the
// objects that need it, and why the first of them does.
type readMiss struct {
	grant   string
	objects []string
	why     string
}

// The reasons a draft needs its author to read an object, what the draft
// shows of it. A direct change is checked against the same live state.
const (
	readWhyServers = "the live config of the servers it names and what each gives a role"
	readWhyRoles   = "the live document of every role it names"
	readWhySets    = "the published text of the sets it names and what they do for each role"
)

// readSentence words the misses of readRefusal, or "" for none: one object
// with the reason its kind gives, several with one reason for all, and the
// next step a person or a token can take. A draft's author is told to ask
// for the grant or to leave the object out of the draft, and the caller of
// a direct route, who sent no draft, to ask for it and make the change
// again.
func (c draftCaller) readSentence(misses []readMiss) string {
	if len(misses) == 0 {
		return ""
	}
	verb, shows := "drafting", "a draft shows"
	if c.Change {
		verb, shows = "changing", "a change is checked against"
	}
	then := func(objects string) string {
		if c.Change {
			return ", then make the change again."
		}
		return ", or leave " + objects + " out of the draft."
	}
	next := "Ask an administrator for that grant"
	if c.adminAPI() {
		next = "Mint a token that also holds that scope with strazactl api-token create"
	}
	if m := misses[0]; len(misses) == 1 && len(m.objects) == 1 {
		return capitalize(fmt.Sprintf("%s %s needs %s, because %s %s. %s%s", verb, m.objects[0], m.grant, shows, m.why, next, then(m.objects[0])))
	}
	if len(misses) > 1 {
		next = strings.NewReplacer("that grant", "those grants", "that scope", "those scopes").Replace(next)
	}
	join := func(words []string, sep string) string {
		return strings.Join(words[:len(words)-1], ", ") + strings.Repeat(sep+" and ", min(1, len(words)-1)) + words[len(words)-1]
	}
	parts := make([]string, len(misses))
	for i, m := range misses {
		parts[i] = verb + " " + join(m.objects, "") + " needs " + m.grant
	}
	return capitalize(join(parts, ",") + ", because " + shows + " the live config of every object it names. " + next + then("those objects"))
}

// capitalize upper-cases the first letter of s.
func capitalize(s string) string {
	return strings.ToUpper(s[:1]) + s[1:]
}
