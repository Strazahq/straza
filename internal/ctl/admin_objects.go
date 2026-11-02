package ctl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// ObjectList is one page of a server's object list as GET
// /v1/admin/apps/{id}/objects answers it. Type is empty on a server that
// pins no object type, and Pinned and Declared stay raw JSON so a later
// revision of the block decodes too.
type ObjectList struct {
	App            string            `json:"app"`
	Type           string            `json:"type"`
	Plural         string            `json:"plural"`
	Attributes     []ObjectAttribute `json:"attributes"`
	Pinned         json.RawMessage   `json:"pinned"`
	Declared       json.RawMessage   `json:"declared"`
	DeclaredFaults []string          `json:"declaredFaults"`
	Drift          bool              `json:"drift"`
	Listing        *ObjectListing    `json:"listing"`
	Objects        []ListedObject    `json:"objects"`
	Total          int               `json:"total"`
	Next           string            `json:"next"`
}

// ObjectAttribute is one attribute the pinned listing keeps and who may set
// it in the source system, org or object.
type ObjectAttribute struct {
	Name  string `json:"name"`
	SetBy string `json:"setBy"`
}

// ObjectListing is the state of a server's background listing: the listing
// tool, whether its listing credential is set, missing or not needed, the
// last outcome with its reason, the version and counts of the list held,
// and when it was last listed, last tried and next due, in RFC 3339.
type ObjectListing struct {
	Tool        string `json:"tool"`
	Credential  string `json:"credential"`
	Outcome     string `json:"outcome"`
	Reason      string `json:"reason"`
	Version     int64  `json:"version"`
	Count       int    `json:"count"`
	Joined      int    `json:"joined"`
	Left        int    `json:"left"`
	ListedAt    string `json:"listedAt"`
	AttemptedAt string `json:"attemptedAt"`
	DueAt       string `json:"dueAt"`
}

// ListedObject is one entry of the object list: its value, id and title,
// the attributes the pinned listing keeps, whether it joined at the current
// version, and the roles that cover it, sorted.
type ListedObject struct {
	Value      string                    `json:"value"`
	ID         string                    `json:"id"`
	Title      string                    `json:"title"`
	Attributes map[string]AttributeValue `json:"attributes"`
	New        bool                      `json:"new"`
	CoveredBy  []string                  `json:"coveredBy"`
}

// AttributeValue is an attribute's value, which the wire sends as one
// string or as a list of strings. Any other JSON value is kept as its text,
// so a later shape does not fail the whole page.
type AttributeValue []string

// UnmarshalJSON reads a string, a list of strings, or any other value as its
// JSON text.
func (v *AttributeValue) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*v = AttributeValue{one}
		return nil
	}
	var many []string
	if json.Unmarshal(b, &many) == nil {
		*v = many
		return nil
	}
	*v = AttributeValue{string(b)}
	return nil
}

// ObjectsRefresh is the answer of a queued listing: the server and when its
// listing is due.
type ObjectsRefresh struct {
	App   string `json:"app"`
	DueAt string `json:"dueAt"`
}

// ListingCredential is the answer of a stored listing credential: the
// server, the first four hex characters of the value's SHA-256 and when it
// was set. The value is never on the wire after the write.
type ListingCredential struct {
	App         string `json:"app"`
	Fingerprint string `json:"fingerprint"`
	SetAt       string `json:"setAt"`
}

// objectsPath is the object list route of the server appRef with the page
// after value after and at most limit entries; an empty after and a zero
// limit leave the server's defaults.
func objectsPath(appRef, after string, limit int) string {
	path := "/v1/admin/apps/" + url.PathEscape(appRef) + "/objects"
	q := url.Values{}
	if after != "" {
		q.Set("after", after)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return path
}

// AppObjects reads one page of a server's object list, the page after the
// value after, with at most limit entries.
func (c *Client) AppObjects(ctx context.Context, appRef, after string, limit int) (ObjectList, error) {
	var out ObjectList
	return out, c.Do(ctx, http.MethodGet, objectsPath(appRef, after, limit), nil, &out)
}

// AppObjectsJSON returns the same page exactly as the server answered it.
func (c *Client) AppObjectsJSON(ctx context.Context, appRef, after string, limit int) ([]byte, error) {
	return c.DoBytes(ctx, http.MethodGet, objectsPath(appRef, after, limit))
}

// RefreshAppObjects asks for a listing of the server's objects now.
func (c *Client) RefreshAppObjects(ctx context.Context, appRef string) (ObjectsRefresh, error) {
	var out ObjectsRefresh
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/apps/"+url.PathEscape(appRef)+"/objects/refresh", nil, &out)
}

// SetListingCredential stores the credential Straza lists the server's
// objects with. The value is sent once over the authenticated channel and
// never echoed back.
func (c *Client) SetListingCredential(ctx context.Context, appRef, value string) (ListingCredential, error) {
	var out ListingCredential
	return out, c.Do(ctx, http.MethodPost, "/v1/admin/apps/"+url.PathEscape(appRef)+"/objects/credential",
		map[string]string{"value": value}, &out)
}

// RemoveListingCredential deletes the server's listing credential.
func (c *Client) RemoveListingCredential(ctx context.Context, appRef string) error {
	return c.Do(ctx, http.MethodDelete, "/v1/admin/apps/"+url.PathEscape(appRef)+"/objects/credential", nil, nil)
}

// DraftAppObjects opens a draft that pins what the server's tool marks
// declare, and answers the body and the status as the server sent them: 201
// with the draft and its verdict, 200 when the pinned block already matches,
// or a refusal.
func (c *Client) DraftAppObjects(ctx context.Context, appRef string) ([]byte, int, error) {
	return c.draftsCall(ctx, http.MethodPost, "/v1/admin/apps/"+url.PathEscape(appRef)+"/objects/draft", nil)
}
