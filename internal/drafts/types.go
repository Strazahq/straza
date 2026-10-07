// Package drafts is the pure core of config drafts: the documents a draft
// carries, and the verdict the server computes when it checks a draft
// against live state. The package reads no store, opens no connection and
// starts no process: every input arrives as a value, so one set of checks
// serves a draft, a direct admin write and an agent's status read alike.
package drafts

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Kind names the kind of config object a draft item carries.
type Kind string

// The kinds a draft may carry. An App is the app.yaml manifest of an MCP
// server, a Role is the spec/objects Role document with its access row and
// what it implies, and a PolicySet is the policy document.
const (
	KindApp       Kind = "App"
	KindRole      Kind = "Role"
	KindPolicySet Kind = "PolicySet"
)

// Op is what an item does to its object when the draft is published.
type Op string

// The item operations: put creates the object or replaces it with the
// item's document, remove deletes it, and off stores a PolicySet turned off
// with the item's document as its text, since turning a set off changes
// enforcement and put and remove cannot say it.
const (
	OpPut    Op = "put"
	OpRemove Op = "remove"
	OpOff    Op = "off"
)

// Fingerprint is the hex sha256 of an object's config fields as live state
// holds them. It is empty when the object does not exist.
type Fingerprint string

// Item is one object a draft creates, changes or removes. Doc is the object's
// canonical document and is empty for a removal. Base is the object's
// fingerprint when the server last checked the draft, so a publish can refuse
// when live state moved since. Number is the number ParseBundle gave the
// item's document, counted from 1 across every text, and 0 for an item that
// came in another form. It is never stored or sent.
type Item struct {
	Kind   Kind        `json:"kind"`
	Name   string      `json:"name"`
	Op     Op          `json:"op"`
	Doc    string      `json:"doc,omitempty"`
	Base   Fingerprint `json:"base,omitempty"`
	Number int         `json:"-"`
}

// Object is the item's display key, such as App/github.
func (it Item) Object() string { return string(it.Kind) + "/" + it.Name }

// Door names how a draft came in.
type Door string

// The doors. A direct admin route makes a one-item draft and publishes it in
// the same request, so its door is api.
const (
	DoorConsole   Door = "console"
	DoorStrazactl Door = "strazactl"
	DoorAgent     Door = "straza-app"
	DoorAppsDir   Door = "apps-directory"
	DoorAPI       Door = "api"
)

// Principal is who wrote a revision of a draft or publishes it, as the server
// authenticated them. Via is login, session, api-token, file or upgrade, and
// Client is the client that made the request.
type Principal struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	Agent       bool   `json:"agent"`
	Via         string `json:"via"`
	Client      string `json:"client"`
	SponsorID   string `json:"sponsor_id,omitempty"`
	SponsorName string `json:"sponsor,omitempty"`
}

// State is where a draft stands.
type State string

// The draft states. Only an open draft can be revised, checked or published.
const (
	StateOpen      State = "open"
	StatePublished State = "published"
	StateDiscarded State = "discarded"
	StateExpired   State = "expired"
)

// Draft is a set of items that publish together or not at all. Note is the
// proposer's own words and is never a check. Authors lists every principal
// that wrote a revision, oldest first. Source is the file path when the apps
// directory proposed the draft, Refusal why its file became no items, a
// sentence and, on a line after it, its fix when it has one, and Reverts is
// the id of the published draft an undo reverses.
type Draft struct {
	ID       string      `json:"id"`
	Revision int         `json:"revision"`
	State    State       `json:"state"`
	Door     Door        `json:"door"`
	Source   string      `json:"source,omitempty"`
	Refusal  string      `json:"refusal,omitempty"`
	Note     string      `json:"note,omitempty"`
	Authors  []Principal `json:"authors"`
	Items    []Item      `json:"items"`
	Reverts  string      `json:"reverts,omitempty"`
}

// Class sorts a finding of the verdict.
type Class string

// The classes. A refusal blocks publish. A risk widens access and needs the
// publisher's acknowledgment. A warning says what will not work yet. An
// unchecked line says what Straza could not check without contacting or
// starting something. Passed and info lines only inform.
const (
	ClassRefused   Class = "refused"
	ClassRisk      Class = "risk"
	ClassWarning   Class = "warning"
	ClassUnchecked Class = "unchecked"
	ClassPassed    Class = "passed"
	ClassInfo      Class = "info"
)

// Ack is how a publisher acknowledges a risk: a tick when republishing the
// old state undoes the change, the typed text when it cannot.
type Ack string

// The acknowledgment kinds.
const (
	AckTick  Ack = "tick"
	AckTyped Ack = "typed"
)

// Finding is one line of a verdict. Before and After describe a change in
// plain words. Typed is the text a publisher types for an AckTyped risk, and
// Fix says what to do about a refusal or a warning. Document is the number
// of the bundle document a refusal of reading it names, counted from 1
// across every text sent, or of the first document of a text it refuses
// whole, so a client that sent the texts can say where it sits. A warning
// that replaces such a refusal keeps its number. It is 0 for every other
// finding.
type Finding struct {
	Code     string `json:"code"`
	Class    Class  `json:"class"`
	Ack      Ack    `json:"ack,omitempty"`
	Object   string `json:"object,omitempty"`
	Sentence string `json:"sentence"`
	Fix      string `json:"fix,omitempty"`
	Before   string `json:"before,omitempty"`
	After    string `json:"after,omitempty"`
	Typed    string `json:"typed,omitempty"`
	Document int    `json:"document,omitempty"`
}

// Key identifies a risk for an acknowledgment. It covers the code, the object
// and the before and after words and never a count, so a membership change
// between review and publish does not void an acknowledgment.
func (f Finding) Key() string {
	sum := sha256.Sum256([]byte(f.Code + "\n" + f.Object + "\n" + f.Before + "\n" + f.After))
	return hex.EncodeToString(sum[:])
}

// Outcome is what a call to a tool does for a holder of a role.
type Outcome string

// The outcomes, in the words the policy pages use.
const (
	OutcomeRuns         Outcome = "runs"
	OutcomeApproval     Outcome = "needs-approval"
	OutcomeDenied       Outcome = "denied"
	OutcomeNotReachable Outcome = "not-reachable"
	OutcomeUnknown      Outcome = "unknown"
)

// Gain is one row of who gains what: a role, a tool on a server, what a
// holder gets today and after publishing, and who holds the role now.
// HolderCount is how many users the row describes, and Holders names them,
// which the server answers only to a reader who may see role membership.
// BeforeWords and AfterWords carry the gate in plain words, such as "a hold,
// up to 2 minutes, decided by sec-approvers".
type Gain struct {
	Role        string   `json:"role"`
	Server      string   `json:"server"`
	Tool        string   `json:"tool"`
	Holders     []string `json:"holders,omitempty"`
	HolderCount int      `json:"holders_count"`
	Before      Outcome  `json:"before"`
	After       Outcome  `json:"after"`
	BeforeWords string   `json:"before_words,omitempty"`
	AfterWords  string   `json:"after_words,omitempty"`
}

// LastChange is the newest publish that changed an object: the published
// draft's id, who published it and when. draft.stale names it.
type LastChange struct {
	Draft     int64
	Publisher string
	At        time.Time
}

// Need is the standing an object in the draft needs from its publisher, in
// words, such as "apps:write or the role straza-global-mcp-admin".
type Need struct {
	Object   string `json:"object"`
	Standing string `json:"standing"`
}

// Verdict is the server's reading of one revision of a draft against live
// state. Snapshot is the policy version it was checked against. RiskDigest
// covers the keys of every risk, and a publish carries it back.
type Verdict struct {
	Draft      string    `json:"draft"`
	Revision   int       `json:"revision"`
	Snapshot   string    `json:"snapshot"`
	CheckedAt  string    `json:"checked_at"`
	Refused    []Finding `json:"refused"`
	Risks      []Finding `json:"risks"`
	Warnings   []Finding `json:"warnings"`
	Unchecked  []Finding `json:"unchecked"`
	Passed     []Finding `json:"passed"`
	Info       []Finding `json:"info"`
	Gains      []Gain    `json:"gains"`
	Needs      []Need    `json:"needs"`
	RiskDigest string    `json:"risk_digest"`
}
