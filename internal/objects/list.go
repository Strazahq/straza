package objects

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// List is one server's object list as a replica holds it: the version and
// objects of the last good listing, sorted by value, and the status of the
// last listing. It never changes once built.
type List struct {
	AppID    string
	Type     string
	DeclHash string
	Version  int64
	Objects  []Object

	Outcome      string
	Reason       string
	Count        int
	Joined, Left int
	ListedAt     *time.Time
	AttemptedAt  *time.Time
	DueAt        time.Time

	byValue map[string]int
	byID    map[string]int
}

// Object is one object of a list: its value, the id the server gives it,
// its title, the attributes the pinned declaration names, and the version
// at which its id joined the list.
type Object struct {
	Value        string
	ID           string
	Title        string
	Attributes   map[string]Attr
	AddedVersion int64
}

// Attr is one attribute value: one string, or a list of strings when List
// is set. Its JSON form is the string or the array.
type Attr struct {
	Values []string
	List   bool
}

// ByValue answers the object whose value is v, byte for byte.
func (l *List) ByValue(v string) (Object, bool) {
	i, ok := l.byValue[v]
	if !ok {
		return Object{}, false
	}
	return l.Objects[i], true
}

// ByValueFold answers the object whose value is v without regard to case,
// and so the value as the list spells it. An object whose value is v byte
// for byte wins. Otherwise exactly one object may equal v under case
// folding, and two or more answer none, because the caller cannot tell
// which one v means. It walks the list, so it suits a lookup per request,
// not per object.
func (l *List) ByValueFold(v string) (Object, bool) {
	if o, ok := l.ByValue(v); ok {
		return o, true
	}
	found, n := -1, 0
	for i := range l.Objects {
		if strings.EqualFold(l.Objects[i].Value, v) {
			found, n = i, n+1
		}
	}
	if n != 1 {
		return Object{}, false
	}
	return l.Objects[found], true
}

// ByID answers the object whose id is id.
func (l *List) ByID(id string) (Object, bool) {
	i, ok := l.byID[id]
	if !ok {
		return Object{}, false
	}
	return l.Objects[i], true
}

// MarshalJSON writes the string, or the array when the value is a list.
func (a Attr) MarshalJSON() ([]byte, error) {
	if a.List {
		if a.Values == nil {
			return []byte("[]"), nil
		}
		return json.Marshal(a.Values)
	}
	if len(a.Values) == 0 {
		return json.Marshal("")
	}
	return json.Marshal(a.Values[0])
}

// UnmarshalJSON reads a string or an array of strings.
func (a *Attr) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*a = Attr{Values: []string{s}}
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return errors.New("objects: an attribute value is neither a string nor a list of strings")
	}
	*a = Attr{Values: list, List: true}
	return nil
}

// withStatus answers a copy of l that keeps its objects and takes the
// status of row.
func (l *List) withStatus(row store.ObjectList) *List {
	c := *l
	c.Outcome, c.Reason, c.Count, c.Joined, c.Left = row.Outcome, row.Reason, row.Count, row.Joined, row.Left
	c.ListedAt, c.AttemptedAt, c.DueAt = row.ListedAt, row.AttemptedAt, row.DueAt
	return &c
}

// newList builds the list of row with objs, sorted by value with both
// lookups.
func newList(row store.ObjectList, objs []Object) *List {
	sort.Slice(objs, func(i, j int) bool { return objs[i].Value < objs[j].Value })
	l := &List{AppID: row.AppID, Type: row.Type, DeclHash: row.DeclHash, Version: row.Version, Objects: objs,
		byValue: make(map[string]int, len(objs)), byID: make(map[string]int, len(objs))}
	for i, o := range objs {
		l.byValue[o.Value] = i
		l.byID[o.ID] = i
	}
	return l.withStatus(row)
}
