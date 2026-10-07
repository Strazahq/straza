package drafts

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// nameFault answers what makes the name of an item read as another name
// would, after "The name of document n", or "": first a character
// oddCharacter words with invisibleInNames, and invisible true, then, in
// order, a whitespace character other than a plain space, or a character
// nameRune refuses, spelled by its code point and invisible true, then a
// space at the start or the end, two spaces in a row, and letters from
// scripts no one writing mixes. Every door runs it on a person's and an
// agent's names alike, and the check waives a name live state holds.
func nameFault(name string) (words string, invisible bool) {
	if words := oddCharacter(name, "", invisibleInNames); words != "" {
		return "holds " + words, true
	}
	for _, r := range name {
		switch {
		case r == ' ':
		case unicode.IsSpace(r):
			return "holds a whitespace character other than a plain space, " + runeWords(r), false
		case !nameRune(r):
			return fmt.Sprintf("holds an invisible character, U+%04X", r), true
		}
	}
	switch {
	case strings.HasPrefix(name, " "):
		return "starts with a space", false
	case strings.HasSuffix(name, " "):
		return "ends with a space", false
	case strings.Contains(name, "  "):
		return "holds two spaces in a row", false
	}
	return mixedScripts(name), false
}

// nameRune reports whether r may sit in a name: a letter, a number, a
// punctuation or symbol character other than the braille blank, a mark
// other than a variation selector or the combining grapheme joiner, or a
// plain space, and never a default ignorable code point, such as a Hangul
// filler. Everything else, a format character, a private use or an
// unassigned code point, prints nothing, so a name holding it can pass for
// another.
func nameRune(r rune) bool {
	switch {
	case r == ' ':
		return true
	case r == 0x2800 || unicode.In(r, unicode.Variation_Selector, unicode.Other_Default_Ignorable_Code_Point):
		return false
	}
	return unicode.In(r, unicode.L, unicode.N, unicode.P, unicode.S, unicode.M)
}

// oneWriting are the sets of scripts one name may mix, because one language
// writes with them together, as the highly restrictive level of UTS 39
// allows: Latin with Japanese, with Korean, and with Chinese written with
// Bopomofo.
var oneWriting = []map[string]bool{
	{"Latin": true, "Han": true, "Hiragana": true, "Katakana": true},
	{"Latin": true, "Han": true, "Hangul": true},
	{"Latin": true, "Han": true, "Bopomofo": true},
}

// mixedScripts words the first two Unicode scripts among the letters of
// name that no writing mixes, in the order they appear, or answers "" when
// the letters read as one script. A letter of the Common or Inherited
// script counts for no script, and a digit or a punctuation mark is not a
// letter, so a name like AR:dev or Zahlung-Ops keeps working.
func mixedScripts(name string) string {
	var seen []string
	for _, r := range name {
		if !unicode.IsLetter(r) || slices.ContainsFunc(seen, func(s string) bool { return unicode.Is(unicode.Scripts[s], r) }) {
			continue
		}
		script := letterScript(r)
		if script == "" {
			continue
		}
		seen = append(seen, script)
		if len(seen) > 1 && !sameWriting(seen) {
			i := slices.IndexFunc(seen, func(s string) bool { return !sameWriting([]string{s, script}) })
			return fmt.Sprintf("holds letters from both %s and %s", seen[max(i, 0)], script)
		}
	}
	return ""
}

// sameWriting reports whether scripts read as one script: they all belong
// to one set of oneWriting.
func sameWriting(scripts []string) bool {
	for _, w := range oneWriting {
		if !slices.ContainsFunc(scripts, func(s string) bool { return !w[s] }) {
			return true
		}
	}
	return false
}

// letterScript names the Unicode script of the letter r, or answers "" for
// a letter of the Common or Inherited script. An ASCII letter is Latin
// without a table lookup, because names are mostly ASCII.
func letterScript(r rune) string {
	if r <= unicode.MaxASCII {
		return "Latin"
	}
	for name, table := range unicode.Scripts {
		if name != "Common" && name != "Inherited" && unicode.Is(table, r) {
			return name
		}
	}
	return ""
}
