package authn

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
)

// GrantType is the JWS typ of a git grant.
const GrantType = "straza-grant+jwt"

// GrantTTL is how long a grant lives.
const GrantTTL = 120 * time.Second

// maxGrantRefs is the most ref updates one push grant names (spec/grant).
// It is a cheap early bound. GrantMaxBytes binds long before it.
const maxGrantRefs = 1000

// GrantMaxBytes is the longest compact grant Straza issues and a resource
// server must accept (spec/grant). A grant travels in the Authorization
// header line, which many proxies cap at 8 KiB, so the cap leaves room for
// "Authorization: DPoP " and the line end.
const GrantMaxBytes = 8000

// ErrGrantTooLarge is the error MintGrant wraps when the signed grant is
// longer than GrantMaxBytes, which a push that names many refs reaches.
var ErrGrantTooLarge = errors.New("the git grant is longer than a grant may be")

// GrantRef is one ref update a push grant names.
type GrantRef struct {
	Ref string `json:"ref"`
	Old string `json:"old"`
	New string `json:"new"`
}

// Grant is the input and output of MintGrant. MintGrant sets JTI, IssuedAt and Expiry.
type Grant struct {
	Audience string // aud: the app's git url as the manifest parser canonicalized it
	Subject  string // sub: the user id
	Session  string // act.ses: the session id
	Harness  string // act.hrn: "<name>/<version>", omitted from act when empty
	Kind     string // act.kind: human, agent or service
	JKT      string // cnf.jkt: from the verified session claims, never from a request
	Server   string // srv: the app's metadata.name
	ObjType  string // obj.type: the tool's pinned object type
	ObjValue string // obj.value: the canonical object value, owner/name for a repo
	ObjID    string // obj.id: the object list's id, as a string
	Op       string // op: "fetch" or "push"
	Refs     []GrantRef
	Decision string // dec: the CloudEvents id of the grant's audit record
	Approval string // apr: the approval request id, omitted when empty
	JTI      string
	IssuedAt time.Time
	Expiry   time.Time
}

// grantClaims is the JSON claim set of a grant, in the order spec/grant
// lists the claims.
type grantClaims struct {
	Iss  string      `json:"iss"`
	Aud  string      `json:"aud"`
	Sub  string      `json:"sub"`
	Act  grantActor  `json:"act"`
	Iat  int64       `json:"iat"`
	Exp  int64       `json:"exp"`
	JTI  string      `json:"jti"`
	CNF  grantCNF    `json:"cnf"`
	Srv  string      `json:"srv"`
	Obj  grantObject `json:"obj"`
	Op   string      `json:"op"`
	Refs []GrantRef  `json:"refs,omitempty"`
	Dec  string      `json:"dec"`
	Apr  string      `json:"apr,omitempty"`
}

type grantActor struct {
	Ses  string `json:"ses"`
	Hrn  string `json:"hrn,omitempty"`
	Kind string `json:"kind"`
}

type grantCNF struct {
	JKT string `json:"jkt"`
}

type grantObject struct {
	Type  string `json:"type"`
	Value string `json:"value"`
	ID    string `json:"id"`
}

// MintGrant signs a git grant with the newest active session key, under a
// protected header with typ GrantType, alg EdDSA and the key's kid, and iss
// set to the service's issuer. It sets JTI, IssuedAt and Expiry, which is
// IssuedAt plus GrantTTL. It refuses, before it signs, a grant that breaks
// a rule of spec/grant: an empty required field, an op other than fetch or
// push, refs on a fetch, more than 1000 refs, or a ref update whose name or
// ids are malformed.
func (s *TokenService) MintGrant(g Grant) (string, Grant, error) {
	if err := checkGrant(g); err != nil {
		return "", Grant{}, err
	}
	g.JTI = uuid.NewString()
	g.IssuedAt = time.Now().UTC().Truncate(time.Second)
	g.Expiry = g.IssuedAt.Add(GrantTTL)

	s.mu.RLock()
	key := s.active
	s.mu.RUnlock()
	signed, err := signGrant(key, s.issuer, g)
	if err != nil {
		return "", Grant{}, err
	}
	if len(signed) > GrantMaxBytes {
		return "", Grant{}, fmt.Errorf("authn: cannot mint the git grant, because it is %d bytes long, and the limit is %d: %w",
			len(signed), GrantMaxBytes, ErrGrantTooLarge)
	}
	return signed, g, nil
}

// signGrant serializes g as a grant claim set under issuer and signs it
// with key. It checks nothing, so the example generator can sign the
// invalid examples too.
func signGrant(key jwk.Key, issuer string, g Grant) (string, error) {
	payload, err := grantPayload(issuer, g)
	if err != nil {
		return "", err
	}
	return signGrantPayload(key, GrantType, payload)
}

// grantPayload serializes g as the JSON claim set of a grant under issuer.
func grantPayload(issuer string, g Grant) ([]byte, error) {
	payload, err := json.Marshal(grantClaims{
		Iss: issuer, Aud: g.Audience, Sub: g.Subject,
		Act: grantActor{Ses: g.Session, Hrn: g.Harness, Kind: g.Kind},
		Iat: g.IssuedAt.Unix(), Exp: g.Expiry.Unix(), JTI: g.JTI,
		CNF: grantCNF{JKT: g.JKT}, Srv: g.Server,
		Obj: grantObject{Type: g.ObjType, Value: g.ObjValue, ID: g.ObjID},
		Op:  g.Op, Refs: g.Refs, Dec: g.Decision, Apr: g.Approval,
	})
	if err != nil {
		return nil, fmt.Errorf("authn: encode git grant: %w", err)
	}
	return payload, nil
}

// signGrantPayload signs payload with key under a protected header that
// carries typ, alg EdDSA and the key's kid.
func signGrantPayload(key jwk.Key, typ string, payload []byte) (string, error) {
	hdr := jws.NewHeaders()
	if err := hdr.Set(jws.TypeKey, typ); err != nil {
		return "", fmt.Errorf("authn: set git grant typ: %w", err)
	}
	signed, err := jws.Sign(payload, jws.WithKey(jwa.EdDSA(), key, jws.WithProtectedHeaders(hdr)))
	if err != nil {
		return "", fmt.Errorf("authn: sign git grant: %w", err)
	}
	return string(signed), nil
}

// checkGrant applies the issuance rules of spec/grant to g. Every refusal
// names the field and the rule, because a refusal here means the caller
// built the grant wrong.
func checkGrant(g Grant) error {
	for _, f := range []struct{ name, value string }{
		{"audience", g.Audience}, {"subject", g.Subject}, {"session", g.Session},
		{"kind", g.Kind}, {"key thumbprint", g.JKT}, {"server", g.Server},
		{"object type", g.ObjType}, {"object value", g.ObjValue}, {"object id", g.ObjID},
		{"decision id", g.Decision},
	} {
		if f.value == "" {
			return grantRefused("its %s is empty", f.name)
		}
	}
	switch g.Kind {
	case "human", "agent", "service":
	default:
		return grantRefused("its kind %q is not human, agent or service", g.Kind)
	}
	switch {
	case g.Op != "fetch" && g.Op != "push":
		return grantRefused("its op %q is neither fetch nor push", g.Op)
	case g.Op == "fetch" && len(g.Refs) > 0:
		return grantRefused("a fetch grant names no refs, and this one names %d", len(g.Refs))
	case len(g.Refs) > maxGrantRefs:
		return grantRefused("it names %d refs, and the limit is %d", len(g.Refs), maxGrantRefs)
	}
	for _, r := range g.Refs {
		if !strings.HasPrefix(r.Ref, "refs/") || len(r.Ref) == len("refs/") {
			return grantRefused("its ref %q is not a full ref name under refs/", r.Ref)
		}
		if !isObjectID(r.Old) {
			return grantRefused("the old id of %q is not lower-case hex of 40 or 64 characters", r.Ref)
		}
		if !isObjectID(r.New) {
			return grantRefused("the new id of %q is not lower-case hex of 40 or 64 characters", r.Ref)
		}
		if len(r.Old) != len(r.New) {
			return grantRefused("the old and new ids of %q differ in length", r.Ref)
		}
	}
	return nil
}

func grantRefused(format string, args ...any) error {
	return fmt.Errorf("authn: cannot mint the git grant, because "+format, args...)
}

// isObjectID reports whether id is a git object id: lower-case hex of 40
// characters for SHA-1 or 64 for SHA-256.
func isObjectID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// errGrantTyped is the refusal a Straza verifier gives a token typed as a
// git grant. Each verifier wraps it in its own prefix.
var errGrantTyped = errors.New("the token is a git grant, and only a git resource server takes one")

// refuseGrantType returns errGrantTyped when the protected header of the
// compact token raw carries the grant typ. typ is a media type, so the
// comparison ignores case and an application/ prefix (RFC 7515 section
// 4.1.9). A token whose header cannot be read is refused as well.
func refuseGrantType(raw string) error {
	msg, err := jws.Parse([]byte(raw), jws.WithCompact())
	if err != nil {
		return fmt.Errorf("read the protected header: %w", err)
	}
	for _, sig := range msg.Signatures() {
		typ, _ := sig.ProtectedHeaders().Type()
		if strings.TrimPrefix(strings.ToLower(typ), "application/") == GrantType {
			return errGrantTyped
		}
	}
	return nil
}
