package drafts

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/strazahq/straza/internal/redact"
)

// secretNames are the words that mark a name as naming a secret. A name
// counts when its last words spell an entry, so GITHUB_TOKEN, apiKey,
// client_secret and REDIS_PASS count and TOKEN_URL does not, or when its
// last word ends in a one-word entry of five letters or more, so PGPASSWORD
// counts and MONKEY, BYPASS and OAUTH do not. The names of env entries,
// headers, registry arguments, query parameters and the words before a
// value in free text all read this one list.
var secretNames = []string{"token", "key", "apikey", "api_key", "secret", "password", "passwd", "pwd", "pass", "passphrase",
	"credential", "credentials", "auth", "sig", "signature", "access_token", "client_secret", "authorization"}

// secretShapes are the credential shapes a draft may not carry: the redact
// battery with a private key header in place of its complete PEM block, and
// the token shapes the redact battery leaves out. Detection matches the
// header, as the sentinel's leak detector does, so a key with no END line is
// refused and a certificate or a public key is not. An OpenAI key is the sk-
// form of its project, service account and admin keys, or sk- and 32 letters
// and digits, so a name such as sk-learn-wrapper is none.
var secretShapes = func() []redact.Pattern {
	out := []redact.Pattern{{Label: "private key", Re: secretKeyHeaderRe}}
	for _, p := range redact.Patterns {
		if p.Re != redact.PEMBlock {
			out = append(out, p)
		}
	}
	return append(out,
		redact.Pattern{Label: "GitHub fine-grained token", Re: regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`)},
		redact.Pattern{Label: "GitLab token", Re: regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`)},
		redact.Pattern{Label: "Slack token", Re: regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
		redact.Pattern{Label: "Anthropic API key", Re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
		redact.Pattern{Label: "OpenAI API key", Re: regexp.MustCompile(`\bsk-(?:(?:proj|svcacct|admin)-[A-Za-z0-9_-]{20,}|[A-Za-z0-9]{32,})`)},
		redact.Pattern{Label: "Google API key", Re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`)},
		redact.Pattern{Label: "JSON web token", Re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)},
		redact.Pattern{Label: "Hugging Face token", Re: regexp.MustCompile(`\bhf_[A-Za-z]{34}`)},
		redact.Pattern{Label: "Stripe live key", Re: regexp.MustCompile(`\b[sr]k_live_[0-9A-Za-z]{24,}`)},
	)
}()

var (
	// secretKeyHeaderRe matches the header of a private key block.
	secretKeyHeaderRe = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`)
	// secretURLRe finds the addresses inside a string, so an address in a
	// description or an argument is read as well as one that fills a field.
	secretURLRe = redact.URLPattern
	// secretRefRe matches a value that names a secret kept elsewhere instead
	// of holding it: a {placeholder}, Straza's {{secret}} or a ${VAR}, with an
	// optional auth scheme word in front as registry header values carry.
	secretRefRe = regexp.MustCompile(`^(?i:(?:bearer|basic|token)\s+)?\$?\{\{?[A-Za-z0-9_.:-]+\}\}?$`)
	// secretRefInRe finds such a reference inside a word, where quotes or
	// the braces of a JSON object may stand around it.
	secretRefInRe = regexp.MustCompile(`\$?\{\{?[A-Za-z0-9_.:-]+\}\}?`)
	// secretIntRe matches an integer.
	secretIntRe = regexp.MustCompile(`^-?[0-9]+$`)
	// secretPathRe matches a path: a root, then a separator further on or a
	// file name with an extension, so /etc/tls/key.pem and ./key.pem are
	// paths and /k9Zx7Qp2Lm4Wn8R is not.
	secretPathRe = regexp.MustCompile(`^(?:/|\.\.?/|~/|[A-Za-z]:[\\/])(?:[^\\/]*[\\/].*|[^\\/]*\.[A-Za-z0-9]{1,8})$`)
	// secretTokenRe matches a word made of the characters of a token.
	secretTokenRe = regexp.MustCompile(`^[A-Za-z0-9/+=_.~-]+$`)
	// secretPlainRe matches a key or a name plain enough to print in a path.
	secretPlainRe = regexp.MustCompile(`^[A-Za-z0-9_$.-]{1,64}$`)
	// secretCamelRe finds where a camelCase name starts a new word.
	secretCamelRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)

	// secretCapability, secretLooksGenerated and secretSwitches are the
	// redact leaf's readings, so the scan and every text the manager masks
	// read a generated secret the same way.
	secretCapability     = redact.Capability
	secretLooksGenerated = redact.Generated
	secretSwitches       = redact.Switches
)

// secretRuleSet says which text rules read a string. secretShapesOnly reads
// the credential shapes, and is all a policy set gets, because its command
// patterns match addresses with wildcards such as https://*:*@host/*.
// secretPasswords adds an address that carries a password, or a credential
// shape as its user name, and secretAddresses adds a query parameter that
// holds a value under a secret's name, a path that carries a random part,
// and a value that follows a word naming a secret in free text.
type secretRuleSet int

const (
	secretShapesOnly secretRuleSet = iota
	secretPasswords
	secretAddresses
)

// secretTextRules reads one string with the rules of set: the credential
// shapes, the words that name a secret before a value, and then the
// addresses it contains, each as secretAddress reads it.
func secretTextRules(v string, set secretRuleSet) []secretRule {
	var out []secretRule
	if labels := secretShapeLabels(v); labels != "" {
		out = append(out, secretRule{codeSecretShape, "holds what looks like " + labels, "Remove it."})
	}
	if set == secretShapesOnly {
		return out
	}
	if set == secretAddresses && len(secretFreeValues(v)) > 0 {
		out = append(out, secretAfterWord)
	}
	for _, u := range secretURLRe.FindAllString(v, -1) {
		rules, _ := secretAddress(u, set)
		out = append(out, rules...)
	}
	return out
}

// secretAddress reads the address u with the rules of set and answers the
// rules it breaks and u with each part they flag masked: user information
// that carries a password or a credential as %5BREDACTED%5D, the form a
// route answers and net/url reads, a path segment secretCapability reads as
// a capability, and a query value under a secret's name, each as
// redact.Mark. An address is read as written, so one that net/url cannot
// parse is read all the same. A user name alone, such as git in
// ssh://git@host, is no secret.
func secretAddress(u string, set secretRuleSet) ([]secretRule, string) {
	scheme, rest, _ := strings.Cut(u, "://")
	rest, fragment, hasFragment := strings.Cut(rest, "#")
	before, query, hasQuery := strings.Cut(rest, "?")
	host, path := before, ""
	if i := strings.IndexByte(before, '/'); i >= 0 {
		host, path = before[:i], before[i:]
	}
	var out []secretRule
	if at := strings.LastIndex(host, "@"); at >= 0 {
		if user := secretUnescape(host[:at]); strings.Contains(user, ":") || secretShapeLabels(user) != "" {
			out = append(out, secretRule{codeSecretUserinfo, "holds an address that carries a password or a credential before its host", "Remove the user name and password from the address."})
			host = url.PathEscape(redact.Mark) + host[at:]
		}
	}
	if set == secretAddresses && secretCapability(path) {
		out = append(out, secretRule{codeSecretPath, "holds an address whose path carries a long random-looking part, which works as a password for anyone who has the address",
			"Remove that part from the address."})
		path = maskPath(path)
	}
	var q strings.Builder
	for set == secretAddresses && query != "" {
		part, sep := query, ""
		if i := strings.IndexAny(query, "&;"); i >= 0 {
			part, sep, query = query[:i], query[i:i+1], query[i+1:]
		} else {
			query = ""
		}
		name, value, _ := strings.Cut(part, "=")
		if n, v := secretUnescape(name), strings.TrimSpace(secretUnescape(value)); secretNamed(n) && v != "" && secretUnescape(value) != redact.Mark && !secretRefRe.MatchString(v) && !secretBenign(v) {
			what := "holds an address with a query parameter that carries a value under a secret's name"
			if secretPrintable(n) {
				what = "holds an address whose query parameter " + n + " carries a value, and that name marks a secret"
			}
			out = append(out, secretRule{codeSecretQuery, what, "Remove the parameter from the address."})
			part = name + "=" + redact.Mark
		}
		q.WriteString(part + sep)
	}
	if len(out) == 0 {
		return nil, u
	}
	masked := scheme + "://" + host + path
	if hasQuery {
		masked += "?" + q.String() + query
	}
	if hasFragment {
		masked += "#" + fragment
	}
	return out, masked
}

// secretAfterWord is the reading of a value that follows a word naming a
// secret: in free text, in a name=value pair, or as the argument after such
// a flag. One rule for all three keeps a place that two of them read to one
// finding.
var secretAfterWord = secretRule{codeSecretValue, "holds a value after a word that names a secret", "Remove the value."}

// secretInValue reports whether v, a value of an env, header or argument
// list, holds a value after a word that names a secret, whatever the
// value's length: a pair such as password=hunter2, "api_key": "hunter2" or
// X-API-Key: hunter2, or a word and the next, as a shell command's
// --password hunter2. Cookie names a secret here as it does for a header.
// Words part at white space, semicolons, ampersands and commas, and a word
// that ends in = is an empty pair. An auth scheme word is read with the word
// after it, so Authorization: Bearer ${TOKEN} holds a reference. A
// reference, a benign value, an address, which the address rules read, and
// a dash-led word that reads as the next flag count as none.
func secretInValue(v string) bool {
	words := strings.FieldsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(";&,", r) })
	trim := func(w string) string { return strings.TrimRight(strings.TrimLeft(w, "\"'([{<"), "\"')]}>,;.") }
	for i, w := range words {
		name, value, next := strings.TrimSuffix(w, ":"), "", i+1
		switch at := strings.IndexAny(w, ":="); {
		case strings.Contains(w, "://"), at == len(w)-1 && w[at] == '=':
			continue
		case at > 0 && at < len(w)-1:
			name, value = w[:at], w[at+1:]
		case next < len(words):
			value, next = words[next], next+1
		}
		if !secretNamed(name) && !strings.EqualFold(strings.Trim(name, "\"'{-"), "cookie") {
			continue
		}
		if scheme := strings.ToLower(trim(value)); (scheme == "bearer" || scheme == "basic" || scheme == "token") && next < len(words) {
			value = words[next]
		}
		held := trim(value)
		if held == "" || strings.Contains(value, "://") || secretRefInRe.MatchString(value) || secretBenign(held) || (strings.HasPrefix(held, "-") && !secretDashValue(held)) {
			continue
		}
		return true
	}
	return false
}

// secretDashValue reports whether arg, which starts with a dash, reads as
// a secret rather than a flag: before any equals sign it holds a letter
// and a digit side by side twice or more, or a character that is not a
// letter, a digit or a dash. So --port, -v, --no-auth and --log-level=debug
// read as flags, and -Hunter2Hunter2 and -x9Kp!2mQ#vL as values.
func secretDashValue(arg string) bool {
	name, _, _ := strings.Cut(arg, "=")
	return secretSwitches(name) >= 2 || strings.IndexFunc(name, func(r rune) bool {
		return r != '-' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z')
	}) >= 0
}

// secretBenign reports whether v, a value under a secret's name, is no
// secret: a word a setting takes, such as true, off or none, an integer, or
// a path.
func secretBenign(v string) bool {
	switch strings.ToLower(v) {
	case "true", "false", "yes", "no", "on", "off", "none":
		return true
	}
	return secretIntRe.MatchString(v) || secretPathRe.MatchString(v)
}

// secretFreeValues answers the values that follow a word naming a secret in
// the free text v, as in "key wJalr..." or "token: ab12...": a word of 16
// or more token characters in which a letter and a digit stand side by side
// at least twice, and that is no reference, no benign value and no address.
func secretFreeValues(v string) []string {
	words := strings.Fields(v)
	var out []string
	for i, w := range words {
		name, value := strings.TrimRight(w, ":="), ""
		if at := strings.IndexAny(w, ":="); at > 0 && at < len(w)-1 {
			name, value = w[:at], w[at+1:]
		} else if i+1 < len(words) {
			value = words[i+1]
		}
		if value == "" || !secretNamed(name) || secretRefRe.MatchString(value) || strings.Contains(value, "://") {
			continue
		}
		value = strings.TrimRight(strings.TrimLeft(value, "\"'([{<"), "\"')]}>,;.")
		if len(value) >= 16 && secretTokenRe.MatchString(value) && !secretBenign(value) && secretSwitches(value) >= 2 {
			out = append(out, value)
		}
	}
	return out
}

func secretUnescape(s string) string {
	if u, err := url.QueryUnescape(s); err == nil {
		return u
	}
	return s
}

// secretShapeLabels names, with an article, the credential shapes v holds,
// joined in one phrase, or returns the empty string. A match whose last 16
// characters use at most two distinct characters is a documentation example,
// such as the Bearer hf_xxxx placeholder of a registry record, and does not
// count. No battery shape is shorter than 16 characters.
func secretShapeLabels(v string) string {
	var labels []string
	for _, p := range secretShapes {
		if slices.ContainsFunc(p.Re.FindAllString(v, -1), secretCounts) {
			article := "a "
			if strings.ContainsRune("AEIOUaeiou", rune(p.Label[0])) {
				article = "an "
			}
			labels = append(labels, article+p.Label)
		}
	}
	if len(labels) < 2 {
		return strings.Join(labels, "")
	}
	return strings.Join(labels[:len(labels)-1], ", ") + " and " + labels[len(labels)-1]
}

// secretCounts reports whether m, a match of a credential shape, counts:
// its last 16 characters use more than two distinct characters.
func secretCounts(m string) bool {
	var seen [256]bool
	distinct := 0
	for i := max(0, len(m)-16); i < len(m); i++ {
		if !seen[m[i]] {
			seen[m[i]], distinct = true, distinct+1
		}
	}
	return distinct > 2
}

// secretNamed reports whether name ends with the words of an entry of
// secretNames, or in a fused entry as secretNames says. Words split at every
// character that is not a letter or a digit, and where a camelCase name
// starts a new word, so apiKey, API_KEY and api-key agree.
func secretNamed(name string) bool {
	words := strings.FieldsFunc(strings.ToLower(secretCamelRe.ReplaceAllString(name, "${1}_${2}")), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, entry := range secretNames {
		tail := strings.Split(entry, "_")
		if len(words) >= len(tail) && slices.Equal(words[len(words)-len(tail):], tail) {
			return true
		}
		if len(tail) == 1 && len(entry) >= 5 && len(words) > 0 && strings.HasSuffix(words[len(words)-1], entry) {
			return true
		}
	}
	return false
}

// secretPrintable reports whether a key or a name may appear in a finding:
// it is short plain text, and no rule reads a secret in it.
func secretPrintable(s string) bool {
	return secretPlainRe.MatchString(s) && secretShapeLabels(s) == "" && !secretLooksGenerated(s)
}
