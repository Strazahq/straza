package drafts

// Refusal is a rule's no: the sentence a direct admin route answers with,
// word for word, and the kind of no, which the route maps to its HTTP
// status.
type Refusal struct {
	Kind     RefusalKind
	Sentence string
}

// RefusalKind sorts a refusal by what stands in the way.
type RefusalKind int

// The refusal kinds, with the status a direct route answers each with: the
// request is wrong in itself (400), an object it names does not exist
// (404), the caller may not make the change (403), live state stands in the
// way (409), or the object it would create exists already (409, where a
// route may name the object in the way). RefusalUnread says the rule needs a
// part of live state the World does not hold: a direct route reads that
// part and asks again, and anything else fails closed on it.
const (
	RefusalInvalid RefusalKind = iota + 1
	RefusalMissing
	RefusalForbidden
	RefusalConflict
	RefusalExists
	RefusalUnread
)

// refused is a Refusal of kind k with the sentence s.
func refused(k RefusalKind, s string) *Refusal {
	return &Refusal{Kind: k, Sentence: s}
}
