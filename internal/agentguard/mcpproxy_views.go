package agentguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// `straza mcp <server>` is the one-server bridge. It fronts the gateway's
// per-server endpoint /mcp/<server>, mirrors that server's tools under their
// own names with their _meta, and mirrors the server's MCP Apps views as
// resources. A chat app that shows views needs all three: a view calls tools
// by the server's own names, and the host finds the view through the tool's
// _meta.ui.resourceUri and resources/read.

// uiExtension is the capability key of the MCP Apps extension.
const uiExtension = "io.modelcontextprotocol/ui"

// serverNameRe is the App manifest's server name rule, a copy of nameRe in
// internal/manager/manifest.go. The operand must match it before it becomes a
// path segment, because "." and ".." would otherwise be cleaned by the
// gateway's mux into the combined /mcp or the site root.
var serverNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// checkServerName refuses an operand that cannot be a server name.
func checkServerName(server string) error {
	if serverNameRe.MatchString(server) {
		return nil
	}
	return fmt.Errorf("the server name %q is not valid. A server name has only lowercase letters, digits and hyphens, "+
		"is at most 64 characters long, and starts and ends with a letter or digit. "+
		"Run `strazactl apps list`, or open MCP servers in the console, to see the names", server)
}

// go-sdk's own error codes for failures inside the client, which it reports
// as a *jsonrpc.Error although no server answered (internal/jsonrpc2/wire.go
// in go-sdk): the client or server closing, and a transport rejection.
const (
	sdkCodeClientClosing = -32003
	sdkCodeServerClosing = -32004
	sdkCodeRejected      = -32005
)

// gatewayAnswered returns the JSON-RPC error the gateway itself sent in err,
// or nil when err carries none or only one of go-sdk's local codes.
func gatewayAnswered(err error) *jsonrpc.Error {
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) {
		return nil
	}
	switch rpcErr.Code {
	case sdkCodeClientClosing, sdkCodeServerClosing, sdkCodeRejected:
		return nil
	}
	return rpcErr
}

// catalogRefused reports whether err is the gateway's refusal of a server
// that is not in the caller's catalog: JSON-RPC error -32602 on the server's
// endpoint, which the gateway answers to every method in that case.
func catalogRefused(err error) bool {
	rpcErr := gatewayAnswered(err)
	return rpcErr != nil && rpcErr.Code == jsonrpc.CodeInvalidParams
}

// gatewayEndpoint returns the gateway URL the proxy dials: the combined /mcp
// when server is empty, else the per-server endpoint /mcp/<server>.
func gatewayEndpoint(serverURL, server string) string {
	if server == "" {
		return serverURL + "/mcp"
	}
	return serverURL + "/mcp/" + url.PathEscape(server)
}

// proxyServerOptions returns the stdio server's options. The combined bridge
// keeps the SDK defaults (nil). The one-server bridge advertises resources and
// the MCP Apps extension from the start, because a host reads capabilities once
// at initialize, before any view is mirrored. Logging stays as in the defaults.
func proxyServerOptions(server string) *mcp.ServerOptions {
	if server == "" {
		return nil
	}
	caps := &mcp.ServerCapabilities{
		Logging:   &mcp.LoggingCapabilities{},
		Resources: &mcp.ResourceCapabilities{ListChanged: true},
	}
	caps.AddExtension(uiExtension, nil)
	return &mcp.ServerOptions{Capabilities: caps}
}

// serverRefusal turns a failed boot dial of the one-server bridge into the
// error the bridge exits with when a retry cannot help, and returns nil for a
// failure worth retrying. A catalog refusal is the gateway's own answer that
// no server of that name is in the caller's catalog. A 404 means the gateway
// has no per-server endpoint at all, which is an older strazad.
func serverRefusal(server, endpoint string, err error) error {
	switch {
	case catalogRefused(err):
		said := strings.TrimRight(strings.TrimSpace(gatewayAnswered(err).Message), ".")
		return fmt.Errorf("%s. If the server name %q is wrong, fix the argument after mcp in this chat app's MCP configuration, then restart the chat app", said, server)
	case errors.Is(err, mcp.ErrSessionMissing):
		return fmt.Errorf("Straza: the gateway has no endpoint for one server at %s, so it likely runs an older Straza server. "+ //nolint:staticcheck // ST1005: deliberate brand prefix, as on the other bridge exits
			"Ask your administrator to upgrade it, or remove %q after mcp in this chat app's MCP configuration to use all your servers through one endpoint, without views", endpoint, server)
	}
	return nil
}

// resyncViews lists the gateway's views and reconciles the mirrored resources.
// A failed listing leaves the mirror as it is, like a failed tool listing.
func (p *mcpProxy) resyncViews(ctx context.Context) {
	views, ok := p.listAllViews(ctx)
	if ok {
		p.reconcileViews(views)
	}
}

// listAllViews walks the gateway's resources/list to completion with the same
// whole-listing retry and session revival as listAllTools. It walks on every
// resync, whatever the session's initialize advertised, so views switched on
// after the bridge connected appear. A gateway answers -32601 (method not
// found) when the server's views are off, and a catalog refusal on the first
// page means the server left the catalog. Both mean no views. ok=false leaves
// the mirror untouched.
func (p *mcpProxy) listAllViews(ctx context.Context) ([]*mcp.Resource, bool) {
	for attempt := 0; attempt < resyncListAttempts; attempt++ {
		cs := p.session()
		var views []*mcp.Resource
		var lastErr error
		for v, err := range cs.Resources(ctx, nil) {
			if err != nil {
				lastErr = err
				break
			}
			views = append(views, v)
		}
		rpcErr := gatewayAnswered(lastErr)
		switch {
		case lastErr == nil:
			return views, true
		case rpcErr != nil && rpcErr.Code == jsonrpc.CodeMethodNotFound,
			len(views) == 0 && catalogRefused(lastErr):
			return nil, true
		case sessionDeadErr(lastErr):
			if _, err := p.revive(ctx, cs); err != nil {
				return nil, false
			}
			continue
		}
		if attempt+1 >= resyncListAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(p.retryPause):
		}
	}
	return nil, false
}

// viewDigest is the sha256 of the canonical JSON of a mirrored resource, so
// reconcileViews re-adds a view only when a mirrored field changed. A marshal
// failure yields digestUnhashable, which always re-adds.
func viewDigest(r *mcp.Resource) string {
	b, err := json.Marshal(r)
	if err != nil {
		return digestUnhashable
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// reconcileViews brings the stdio server's resources in line with the views
// the gateway lists: add new, drop vanished, re-add on a changed field. A URI
// that url.Parse refuses is skipped and logged, because AddResource panics on
// it.
func (p *mcpProxy) reconcileViews(views []*mcp.Resource) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.views == nil {
		p.views = map[string]string{}
	}
	seen := map[string]bool{}
	added := 0
	for _, v := range views {
		if _, err := url.Parse(v.URI); err != nil {
			p.log("views", fmt.Sprintf("skipped the view %q that the gateway listed, because its URI does not parse: %v", v.URI, err))
			continue
		}
		seen[v.URI] = true
		res := &mcp.Resource{URI: v.URI, Name: v.Name, Title: v.Title,
			Description: v.Description, MIMEType: v.MIMEType, Meta: v.Meta}
		digest := viewDigest(res)
		if p.views[v.URI] == digest && digest != digestUnhashable {
			continue
		}
		p.srv.AddResource(res, p.readView)
		p.views[v.URI] = digest
		added++
	}
	var gone []string
	for uri := range p.views {
		if !seen[uri] {
			gone = append(gone, uri)
			delete(p.views, uri)
		}
	}
	if len(gone) > 0 {
		p.srv.RemoveResources(gone...)
	}
	if p.tr.DebugOn() {
		p.tr.Debug("mcp.views", slog.Int("added", added), slog.Int("removed", len(gone)))
	}
}

// readView answers a host's resources/read by reading the same URI through the
// live gateway session and returning the gateway's result unchanged. A read
// changes nothing, so after a session revival it is replayed whatever the
// original failure was. A JSON-RPC error the gateway sent, such as a view the
// caller may not see, passes through with its code and sentence. Any other
// failure answers -32603 with the bridge's own sentence.
func (p *mcpProxy) readView(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	params := &mcp.ReadResourceParams{URI: req.Params.URI}
	cs := p.session()
	out, err := cs.ReadResource(ctx, params)
	if err != nil && sessionDeadErr(err) {
		fresh, rerr := p.revive(ctx, cs)
		if rerr != nil {
			err = fmt.Errorf("%v; gateway reconnect also failed: %v", err, rerr)
		} else {
			out, err = fresh.ReadResource(ctx, params)
		}
	}
	if err == nil {
		return out, nil
	}
	if rpcErr := gatewayAnswered(err); rpcErr != nil {
		return nil, rpcErr
	}
	return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: fmt.Sprintf(
		"Straza: could not read the view %s through the gateway: %v. Try again, and run `straza doctor` if it keeps failing", params.URI, err)}
}
