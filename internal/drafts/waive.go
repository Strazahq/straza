package drafts

import (
	"cmp"
	"fmt"
	"strings"
)

// waivable are the codes of the refusals Waive may downgrade.
var waivable = map[string]bool{codeSecretValue: true, codeBundleNameChars: true, codeAgentASCIIName: true, codeBundleMasked: true}

// Waive answers the intake findings fs of d read against live state w: a
// refusal that says nothing live state does not hold already becomes a
// warning that says so, and every other finding stays as it is. Two
// refusals qualify. A secret.value refusal of an App put whose flagged
// argument or env value equals, byte for byte, the value at the same place
// of the same server's stored manifest, because the draft then exposes
// nothing new. A name refusal, bundle.name-chars or agent.ascii-name, of
// an item whose name equals, byte for byte, the name of a live object of
// its kind, because the change goes ahead under the stored name. Intake
// keeps judging new text without live state, so a document that
// introduces such a value or name is still refused, and a waived value is
// still masked wherever the scan flagged it.
func Waive(w World, d Draft, fs []Finding) []Finding {
	waived := map[Finding]Finding{}
	for i, it := range d.Items {
		for f, warned := range liveNamed(w, i+1, it) {
			waived[f] = warned
		}
		for f, warned := range liveValued(w, it) {
			waived[f] = warned
		}
	}
	if len(waived) == 0 {
		return fs
	}
	out := make([]Finding, len(fs))
	for i, f := range fs {
		if warned, ok := waived[f]; ok {
			f = warned
		}
		out[i] = f
	}
	return out
}

// Waivable reports whether fs holds only findings Waive may downgrade, so
// a door that runs intake before it reads live state knows whether the
// read can change its answer.
func Waivable(fs []Finding) bool {
	for _, f := range fs {
		if !waivable[f.Code] {
			return false
		}
	}
	return true
}

// liveNamed answers, for item n of a draft whose name is the name of a
// live object of its kind, the name refusals intake makes of it, each with
// the warning that replaces it. The refusals are made again here, by the
// rules intake runs, so a refusal matches only when it reads word for
// word as intake wrote it. The agent rule names the item's own name first,
// and only that finding is waived, because a name the document introduces
// is new text.
func liveNamed(w World, n int, it Item) map[Finding]Finding {
	word := secretKindWords[it.Kind]
	switch it.Kind {
	case KindApp:
		if _, ok := w.Apps[it.Name]; !ok {
			return nil
		}
	case KindRole:
		if _, ok := w.Roles[it.Name]; !ok {
			return nil
		}
	default:
		return nil
	}
	out := map[Finding]Finding{}
	stored := fmt.Sprintf("and the stored name of that %s already holds it, so the change goes ahead under the stored name.", word)
	fix := "Nothing needs to change. A plain name takes a removal and a new object under the new name."
	if words, invisible := nameFault(it.Name); words != "" {
		same := fmt.Sprintf("as the stored name of that %s does, so the change goes ahead under the stored name.", word)
		if invisible {
			same = stored
		}
		for _, f := range itemIntake(n, it, map[string]int{}) {
			if f.Code == codeBundleNameChars {
				out[f] = Finding{Code: f.Code, Class: ClassWarning, Object: f.Object, Document: f.Document,
					Sentence: fmt.Sprintf("The name of document %d %s, %s", cmp.Or(it.Number, n), words, same), Fix: fix}
			}
		}
	}
	if !plainName(it.Name) {
		for _, f := range agentItem(it, nil) {
			if f.Code == codeAgentASCIIName {
				out[f] = Finding{Code: f.Code, Class: ClassWarning, Object: f.Object,
					Sentence: fmt.Sprintf("The name %s holds a character outside a-z, A-Z, 0-9, hyphen, dot and underscore, %s", visible(it.Name), stored), Fix: fix}
				break
			}
		}
	}
	return out
}

// liveValued answers, for an App put of a live server, the secret.value
// refusals of its arguments and env values that the server's stored
// manifest holds at the same place, under the same flag or name, byte for
// byte, each with the warning that replaces it. The draft's document is
// read as the scan reads it, so a refusal matches only when it reads word
// for word as intake wrote it. A place that holds two values in either
// document is never waived. The warning names no server, because the scan
// withholds a name that looks like a secret.
func liveValued(w World, it Item) map[Finding]Finding {
	live, ok := w.Apps[it.Name]
	if it.Kind != KindApp || it.Op != OpPut || !ok || strings.TrimSpace(it.Doc) == "" {
		return nil
	}
	s, set, _ := itemScan(it)
	s.places = map[string][]string{}
	s.read(set)
	stored := secretPlaces(live.Manifest)
	out := map[Finding]Finding{}
	for _, h := range s.kept() {
		held, kept := s.places[h.where], stored[h.where]
		if h.f.Code != codeSecretValue || len(held) != 1 || len(kept) != 1 || held[0] != kept[0] {
			continue
		}
		out[h.f] = Finding{Code: h.f.Code, Class: ClassWarning, Object: h.f.Object,
			Sentence: fmt.Sprintf("In %s, %s %s. The server's stored manifest already holds the same value there, so the draft exposes nothing new.", s.noun, h.where, h.what),
			Fix:      "If it is a secret, " + strings.ToLower(h.f.Fix[:1]) + h.f.Fix[1:] + " If it is not a secret, nothing needs to change."}
	}
	return out
}
