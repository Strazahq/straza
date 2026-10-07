package manager

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ViewMIMEType is the media type of an MCP Apps view: an HTML document a
// chat host renders in a sandboxed frame next to a tool's result.
const ViewMIMEType = "text/html;profile=mcp-app"

// View is one ui:// resource of an upstream server that a listed tool links
// to through its _meta. The gateway serves it on the server's own endpoint to
// a caller whose catalog holds a linking tool.
type View struct {
	URI         string // exactly as the upstream publishes it
	Name        string
	Title       string
	Description string
	MIMEType    string // always ViewMIMEType
	Text        string // the HTML document
	// Meta is the resource's _meta, verbatim (ui.csp, ui.prefersBorder).
	Meta map[string]any
}

// The MCP Apps extension id, the keys a tool links a view by, and the bounds
// on what one server may put in front of people. The bounds are fixed, not
// settings.
const (
	viewsExtension = "io.modelcontextprotocol/ui"
	uiKey          = "ui"
	uiLinkKey      = "resourceUri"
	flatLinkKey    = "ui/resourceUri" // the flat key many servers still write
	viewScheme     = "ui://"

	maxViewBytes    = 2 << 20
	maxViews        = 32
	viewsMaxAge     = 10 * time.Minute
	viewReadTimeout = 15 * time.Second
	// viewKeepAge is how long after the server last answered for a view a
	// read that gets no answer still keeps its copy. Reads come every
	// viewsMaxAge, so a copy outlives one read with no answer and goes at
	// the next, about 20 minutes after the last answer at worst.
	viewKeepAge = 2 * viewsMaxAge
)

// The warnings for a linked view: one the server answered and that fails a
// check, one no answer came for, and one whose copy grew too old.
const (
	viewNotServed = "manager: a view of an MCP server is not served, so the tools that link it are listed without the link. " +
		"An administrator fixes the server, then runs strazactl apps recheck for it, and Straza also reads the view again within 10 minutes"
	viewNotRead = "manager: a view of an MCP server was not read, so Straza serves the copy the server last answered, for at most 20 minutes " +
		"after that answer, and a tool that links a view it never read is listed without the link. " +
		"Straza reads the view again at the next strazactl apps recheck or within 10 minutes"
	viewExpired = "manager: a view of an MCP server is no longer served, because the server has not answered a read of it for 20 minutes, " +
		"so the tools that link it are listed without the link. An administrator checks the server, then runs strazactl apps recheck for it"
)

// viewRead is one read of a server's views: the views it serves, sorted by
// URI, and when the server last answered for each.
type viewRead struct {
	views    []View
	answered map[string]time.Time
}

// viewsOn reports whether straza.exposure.views lets the server show its
// MCP Apps views.
func (m Manifest) viewsOn() bool {
	return m.Straza.Exposure != nil && m.Straza.Exposure.Views
}

// refreshViews answers the views the exposed tools of inst link, with the
// switch on, and nothing with it off. It reads them with the app-level
// secret when the set of links changed, when the last read is older than
// viewsMaxAge, or when a request asked for the probe, and answers the last
// read otherwise, because the probe runs every health tick. A read is
// stored on inst with the links it covers. When ctx ended during the read,
// it answers the last read and stores nothing, since the end of ctx says
// nothing about the server.
func (m *Manager) refreshViews(ctx context.Context, inst *instance, tools []*mcp.Tool, secret *Secret) []View {
	if !inst.manifest.viewsOn() {
		return nil
	}
	uris := linkedURIs(filterTools(tools, inst.manifest.ExposedTools()))
	key := strings.Join(uris, "\n")
	inst.mu.Lock()
	cached := inst.viewCache
	fresh := key == inst.viewLinks && !inst.viewsRead.IsZero() && time.Since(inst.viewsRead) < viewsMaxAge
	inst.mu.Unlock()
	if fresh && ctx.Value(askedKey{}) == nil {
		return cached.views
	}
	read := m.readViews(ctx, inst, uris, secret, cached)
	if ctx.Err() != nil {
		return cached.views
	}
	inst.mu.Lock()
	inst.viewCache, inst.viewLinks, inst.viewsRead = read, key, time.Now()
	inst.mu.Unlock()
	return read.views
}

// readViews reads each linked uri, sorted, within one viewReadTimeout for
// the whole pass, so a server that does not answer holds its probe, and the
// serial health loop, for that long at most. It answers the views that
// pass, at most maxViews, sorted by URI. A view the server answered and that
// fails a check is dropped. A view no answer came for, a uri the pass did
// not reach in time included, keeps its copy in prev while the server
// answered for it within viewKeepAge and the served set has room. Each
// failure is logged once with its reason. Names, titles and descriptions
// come from the server's resource list when it lists the uri there, and a
// failed list only costs those.
func (m *Manager) readViews(ctx context.Context, inst *instance, uris []string, secret *Secret, prev viewRead) viewRead {
	out := viewRead{answered: map[string]time.Time{}}
	if len(uris) == 0 {
		return out
	}
	pass, cancel := context.WithTimeout(ctx, viewReadTimeout)
	defer cancel()
	listed := map[string]*mcp.Resource{}
	if resources, err := inst.runtime.Resources(pass, secret); err == nil {
		for _, r := range resources {
			listed[r.URI] = r
		}
	}
	reads := 0
	for _, uri := range uris {
		if ctx.Err() != nil {
			return out // the caller keeps its last read, so nothing is logged
		}
		line, reason, answered := viewNotServed, "", true
		switch {
		case !strings.HasPrefix(uri, viewScheme):
			reason = "the address is not a ui:// address, and a view must have one"
		case reads == maxViews || len(out.views) == maxViews:
			reason = fmt.Sprintf("the server links more than %d views, and Straza serves at most %d views of one server", maxViews, maxViews)
		case pass.Err() != nil:
			reason, answered = fmt.Sprintf("the read of the server's views ran out of its %s before this view", viewReadTimeout), false
		default:
			reads++
			var v View
			if v, reason, answered = readView(pass, inst.runtime, secret, uri, listed[uri]); reason == "" {
				out.views = append(out.views, v)
				out.answered[uri] = time.Now()
				continue
			}
		}
		if !answered {
			line = viewNotRead
			i, ok := slices.BinarySearchFunc(prev.views, uri, byURI)
			switch at := prev.answered[uri]; {
			case ok && time.Since(at) < viewKeepAge:
				out.views = append(out.views, prev.views[i])
				out.answered[uri] = at
			case ok:
				line = viewExpired
			}
		}
		m.opts.Log.Warn(line, "app", inst.app.Name, "uri", shown(uri), "reason", reason)
	}
	return out
}

// readView reads one view and checks it: one text content item of
// ViewMIMEType, at most maxViewBytes. It answers the reason in plain words
// when the view fails, and an empty reason when it passes. answered is
// false when the failure is no answer of the server: a timeout, a dropped
// connection or one of the SDK's own codes.
func readView(ctx context.Context, rt Runtime, secret *Secret, uri string, listed *mcp.Resource) (_ View, reason string, answered bool) {
	res, err := rt.ReadResource(ctx, secret, uri)
	switch {
	case err != nil:
		return View{}, "reading it from the server failed: " + shown(err.Error()), serverAnswered(err)
	case len(res.Contents) != 1 || res.Contents[0] == nil:
		return View{}, fmt.Sprintf("the server answered %d content items, and a view must be exactly one HTML document", len(res.Contents)), true
	}
	c := res.Contents[0]
	switch {
	case c.Blob != nil:
		return View{}, "the server answered it as binary data, and a view must be one HTML text", true
	case c.MIMEType != ViewMIMEType:
		return View{}, fmt.Sprintf("the server answered it as %q, and a view must be text of type %s", c.MIMEType, ViewMIMEType), true
	case len(c.Text) > maxViewBytes:
		return View{}, fmt.Sprintf("it is %d bytes, and a view may be at most %d bytes", len(c.Text), maxViewBytes), true
	}
	v := View{URI: uri, Name: viewName(uri), MIMEType: ViewMIMEType, Text: c.Text, Meta: c.Meta}
	if listed != nil {
		v.Name, v.Title, v.Description = cmp.Or(listed.Name, v.Name), listed.Title, listed.Description
		if len(v.Meta) == 0 {
			v.Meta = listed.Meta
		}
	}
	return v, "", true
}

// go-sdk's own error codes for failures inside the client, which it reports
// as a *jsonrpc.Error although no server answered: the client or the server
// closing, and a transport rejection.
const (
	sdkCodeClientClosing = -32003
	sdkCodeServerClosing = -32004
	sdkCodeRejected      = -32005
)

// serverAnswered reports whether err is a JSON-RPC error the server sent,
// not a timeout, a transport failure or one of go-sdk's local codes.
func serverAnswered(err error) bool {
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) {
		return false
	}
	switch rpcErr.Code {
	case sdkCodeClientClosing, sdkCodeServerClosing, sdkCodeRejected:
		return false
	}
	return true
}

// changedViews answers, sorted, the URIs of the views that appear in only
// one of prev and next, or in both with another document or _meta.
func changedViews(prev, next []View) []string {
	was := make(map[string]View, len(prev))
	for _, v := range prev {
		was[v.URI] = v
	}
	var out []string
	for _, v := range next {
		p, ok := was[v.URI]
		delete(was, v.URI)
		if !ok || !sameView(p, v) {
			out = append(out, v.URI)
		}
	}
	for uri := range was {
		out = append(out, uri)
	}
	slices.Sort(out)
	return out
}

// driftDetail words a drift for the server's health reason: the tools added
// and removed, else the views that changed, else a change of the views the
// tools link, else the bare lists.
func driftDetail(added, removed, views []string, links bool) string {
	switch {
	case len(added) > 0 || len(removed) > 0:
	case len(views) > 0:
		return "inventory drift: the server changed the views " + strings.Join(views, ", ")
	case links:
		return "inventory drift: the server changed which views its tools open"
	}
	return fmt.Sprintf("inventory drift: +%v -%v", added, removed)
}

// linksChanged reports whether a tool of next links views by other _meta
// than the tool of the same name in prev.
func linksChanged(prev, next []*mcp.Tool) bool {
	was := make(map[string]string, len(prev))
	for _, t := range prev {
		was[t.Name] = linkMeta(t)
	}
	for _, t := range next {
		if l, ok := was[t.Name]; ok && l != linkMeta(t) {
			return true
		}
	}
	return false
}

// linkMeta is the part of a tool's _meta that links views, as JSON.
func linkMeta(t *mcp.Tool) string {
	b, _ := json.Marshal([]any{t.Meta[uiKey], t.Meta[flatLinkKey]})
	return string(b)
}

// sameView reports whether two copies of a view carry one document and one
// _meta, the parts inventoryHash covers.
func sameView(a, b View) bool {
	ma, _ := json.Marshal(a.Meta)
	mb, _ := json.Marshal(b.Meta)
	return a.Text == b.Text && bytes.Equal(ma, mb)
}

// byURI orders a view against a URI, for a binary search of views sorted by
// URI.
func byURI(v View, uri string) int { return strings.Compare(v.URI, uri) }

// viewName is the last path segment of a view's address, the name of a view
// the server does not list, or the whole address when that segment is empty.
func viewName(uri string) string {
	if name := uri[strings.LastIndex(uri, "/")+1:]; name != "" {
		return name
	}
	return uri
}

// toolLinks answers the addresses a tool links views by, under _meta.ui
// resourceUri and under the flat key, in that order. A value that is not a
// string links nothing.
func toolLinks(t *mcp.Tool) []string {
	var out []string
	if ui, ok := t.Meta[uiKey].(map[string]any); ok {
		if uri, ok := ui[uiLinkKey].(string); ok {
			out = append(out, uri)
		}
	}
	if uri, ok := t.Meta[flatLinkKey].(string); ok {
		out = append(out, uri)
	}
	return out
}

// linkedURIs answers, sorted and once each, the addresses tools link.
func linkedURIs(tools []*mcp.Tool) []string {
	var out []string
	for _, t := range tools {
		out = append(out, toolLinks(t)...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// servedTools answers tools as a catalog lists them, writing over the
// entries of tools, which must be the caller's own slice. With the switch
// off, no tool carries the ui key or the flat key. With it on, a tool keeps
// a link only to a view in served, which is sorted by URI. A tool that
// changes is a copy, so the cached inventory is never written.
func servedTools(tools []*mcp.Tool, served []View, on bool) []*mcp.Tool {
	for i, t := range tools {
		if meta, changed := servedMeta(t.Meta, served, on); changed {
			cp := *t
			cp.Meta = meta
			tools[i] = &cp
		}
	}
	return tools
}

// servedMeta answers a tool's _meta as servedTools serves it, and whether
// that differs from meta, which it never writes.
func servedMeta(meta mcp.Meta, served []View, on bool) (mcp.Meta, bool) {
	ui, hasUI := meta[uiKey]
	flat, hasFlat := meta[flatLinkKey]
	if !hasUI && !hasFlat {
		return meta, false
	}
	if !on {
		out := maps.Clone(meta)
		delete(out, uiKey)
		delete(out, flatLinkKey)
		return out, true
	}
	var out mcp.Meta
	if hasFlat && !isServed(flat, served) {
		out = maps.Clone(meta)
		delete(out, flatLinkKey)
	}
	if m, ok := ui.(map[string]any); ok {
		if link, has := m[uiLinkKey]; has && !isServed(link, served) {
			if out == nil {
				out = maps.Clone(meta)
			}
			kept := maps.Clone(m)
			delete(kept, uiLinkKey)
			out[uiKey] = kept
		}
	}
	if out == nil {
		return meta, false
	}
	return out, true
}

// isServed reports whether link is the address of a view in served, which is
// sorted by URI.
func isServed(link any, served []View) bool {
	uri, ok := link.(string)
	if !ok {
		return false
	}
	_, found := slices.BinarySearchFunc(served, uri, byURI)
	return found
}
