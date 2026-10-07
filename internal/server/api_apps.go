package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/strazahq/straza/internal/drafts"
	"github.com/strazahq/straza/internal/manager"
	"github.com/strazahq/straza/internal/store"
)

// appPayload is the admin API app representation (pkg/api/openapi.yaml).
type appPayload struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Runtime string   `json:"runtime"`
	Status  string   `json:"status"`
	Detail  string   `json:"detail,omitempty"`
	Source  string   `json:"source"`
	Tools   []string `json:"tools,omitempty"`
	// Health observability: both omitted until the manager has probed
	// the app; last_healthy_at stays omitted while it has never been healthy.
	LastProbeAt   string `json:"last_probe_at,omitempty"`
	LastHealthyAt string `json:"last_healthy_at,omitempty"`
	// StatusSince is when the status word last changed (0.94.0), so the
	// console can say how long a server has read degraded; omitted until
	// the manager has set a status.
	StatusSince string `json:"status_since,omitempty"`
	// Paused is the admin-pause marker (sourced from manager.IsPaused, not
	// the AppView): true = deliberately disabled by an admin and immune to
	// GitOps re-adoption, distinguishing it from a crash-stopped app. Omitted
	// when false.
	Paused bool `json:"paused,omitempty"`
	// ReachedBy lists, sorted, the roles holding an access row on the app;
	// an empty array when none.
	ReachedBy []string `json:"reached_by"`
	// Manifest is the installed manifest as a JSON object of the stored
	// shape (the app.yaml shape: apiVersion, kind, metadata, server,
	// straza), masked as every route answers a server's document, and URL
	// the remote runtime's address as that masked manifest reads it
	// (absent on command and oci). See manifestFields.
	Manifest json.RawMessage `json:"manifest,omitempty"`
	URL      string          `json:"url,omitempty"`
	// File is the full path of the present apps directory file that names
	// the server, absent when none does, and FileDiffers is true when that
	// file's manifest differs from the live one or does not read (listApps
	// only).
	File        string `json:"file,omitempty"`
	FileDiffers bool   `json:"file_differs,omitempty"`
	// Offered is every tool the server itself lists, before the manifest's
	// exposure list (0.98.0, listApps only). Tools stays the exposed set,
	// and a server with no live instance offers nothing.
	Offered []string `json:"offered,omitempty"`
	// AdminRole names the control-plane role that administers this server
	// and AdminRoleID its id (0.104.0); every server has one.
	AdminRole   string `json:"admin_role,omitempty"`
	AdminRoleID string `json:"admin_role_id,omitempty"`
	// MayChange says whether the caller may change this server (0.104.0,
	// listApps only): true under an area grant, else true on the servers
	// whose admin role the session holds. Display only.
	MayChange *bool `json:"may_change,omitempty"`
}

// withAdminRole fills the payload's admin role fields from the row, with
// names resolved once for a list or looked up for one answer.
func (a *App) withAdminRole(ctx context.Context, p *appPayload, row store.App, names map[string]string) {
	p.AdminRoleID = row.AdminRoleID
	if names != nil {
		p.AdminRole = names[row.AdminRoleID]
		return
	}
	if role, err := a.store.Roles().GetByID(ctx, row.AdminRoleID); err == nil {
		p.AdminRole = role.Name
	}
}

// reachedBy maps every app id to the sorted names of the roles holding an
// access row on it (admin plane: store reads).
func (a *App) reachedBy(ctx context.Context) (map[string][]string, error) {
	bindings, err := a.store.ToolBindings().List(ctx)
	if err != nil {
		return nil, err
	}
	roles, err := a.store.Roles().List(ctx)
	if err != nil {
		return nil, err
	}
	name := make(map[string]string, len(roles))
	for _, ro := range roles {
		name[ro.ID] = ro.Name
	}
	out := map[string][]string{}
	for _, b := range bindings {
		if n := name[b.RoleID]; n != "" {
			out[b.AppID] = append(out[b.AppID], n)
		}
	}
	for _, names := range out {
		sort.Strings(names)
	}
	return out, nil
}

func appPayloadFromView(v manager.AppView) appPayload {
	p := appPayload{
		ID: v.ID, Name: v.Name, Version: v.Version,
		Runtime: v.Runtime, Status: v.Status, Detail: v.Detail, Source: v.Source,
	}
	for _, t := range v.Tools {
		p.Tools = append(p.Tools, t.Name)
	}
	if !v.LastProbe.IsZero() {
		p.LastProbeAt = v.LastProbe.UTC().Format(time.RFC3339)
	}
	if !v.LastHealthy.IsZero() {
		p.LastHealthyAt = v.LastHealthy.UTC().Format(time.RFC3339)
	}
	if !v.StatusSince.IsZero() {
		p.StatusSince = v.StatusSince.UTC().Format(time.RFC3339)
	}
	return p
}

// appPayloadFromRow is the payload of an app with no live instance, a
// stopped or paused one, read from its stored row.
func appPayloadFromRow(row store.App) appPayload {
	return appPayload{
		ID: row.ID, Name: row.Name, Version: row.Version,
		Runtime: row.RuntimeKind, Status: row.Status, Source: row.Source,
	}
}

// handleAppsList merges persisted rows with live manager state.
func (a *App) handleAppsList(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.Apps().List(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list apps failed", err)
		return
	}
	reached, err := a.reachedBy(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list bindings failed", err)
		return
	}
	roles, err := a.store.Roles().List(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "list roles failed", err)
		return
	}
	names := make(map[string]string, len(roles))
	for _, ro := range roles {
		names[ro.ID] = ro.Name
	}
	st := standingFrom(r.Context())
	out := make([]appPayload, 0, len(rows))
	for _, row := range rows {
		if !st.Full && !st.Apps[row.ID] {
			continue
		}
		var p appPayload
		if v, ok := a.manager.View(row.Name); ok {
			p = appPayloadFromView(v)
			// The offered list feeds the console's exposure picker, so the
			// list carries it and the other app answers do not.
			p.Offered = v.Offered
		} else {
			p = appPayloadFromRow(row)
		}
		p.Paused = a.manager.IsPaused(row.Name)
		p.ReachedBy = nonNilStrings(reached[row.ID])
		p.Manifest, p.URL = manifestFields(row.Manifest)
		p.File, p.FileDiffers = a.fileOf(row)
		a.withAdminRole(r.Context(), &p, row, names)
		may := st.Full || st.Apps[row.ID]
		p.MayChange = &may
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAppsInstall accepts a raw app.yaml body (application/yaml), validates
// it against the spec, and publishes it as a one-item draft. An
// oauth manifest naming a provider this server does not have is refused
// with 422, dry run or not. With dryRun=1 it then stops and answers what the
// manifest declares, so a console can check as the operator types and a
// GitOps check can validate a manifest. A manifest equal to the stored one
// changes nothing and restarts nothing.
func (a *App) handleAppsInstall(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		apiError(w, http.StatusBadRequest, "read body failed")
		return
	}
	mf, err := manager.Parse(raw)
	if err != nil {
		apiError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	name := mf.Metadata.Name
	if r.URL.Query().Get("dryRun") == "1" {
		var world drafts.World
		var prev store.App
		if !standingFrom(r.Context()).Full {
			if prev, err = a.store.Apps().GetByName(r.Context(), name); err == nil {
				world = serverWorld(prev)
			}
		}
		if a.installRefused(w, r, raw, mf, world, prev) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"name": name, "runtime": mf.Straza.Runtime.Kind,
			"credential": mf.CredentialKind(), "tools": nonNilStrings(mf.ExposedTools()),
		})
		return
	}
	readFailed := "install failed: Straza could not read the stored record of " + name +
		", so it cannot tell a change from a first install. Try again, and check the strazad log if it keeps failing."
	res, ok := a.publishOne(w, r, directRoute{
		prepare: func(live drafts.World, _ drafts.Principal) (directChange, bool) {
			app := live.Apps[name]
			if a.installRefused(w, r, raw, mf, live, store.App{Name: app.Name, Manifest: app.Manifest, RuntimeKind: app.Runtime}) {
				return directChange{}, false
			}
			return directChange{Item: drafts.Item{Kind: drafts.KindApp, Name: name, Op: drafts.OpPut, Doc: string(raw)}}, true
		},
		bodyStatus: http.StatusUnprocessableEntity,
		failed:     func(error) string { return readFailed },
	})
	if !ok {
		return
	}
	// The row holds the new manifest even when its runtime failed to start,
	// and its records committed with it.
	if err := res.Started[name]; err != nil {
		a.fail(w, r, http.StatusInternalServerError, "install failed: "+err.Error(), err)
		return
	}
	row, err := a.store.Apps().GetByName(r.Context(), name)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Straza installed "+name+" but could not read it back to answer. "+
			"List the servers with strazactl apps list to see its status.", err)
		return
	}
	// A paused app took the manifest at rest and has no live instance, so
	// the answer is its stored row.
	p := appPayloadFromRow(row)
	if v, ok := a.manager.View(row.Name); ok {
		p = appPayloadFromView(v)
	}
	p.Paused = a.manager.IsPaused(row.Name)
	a.withAdminRole(r.Context(), &p, row, nil)
	writeJSON(w, http.StatusCreated, p)
}

// installRefused runs the refusals of an install of mf, read from raw,
// before any draft, in today's order, and answers the request itself and
// reports true when one holds: a server admin, who changes the servers
// they administer and registers nothing, is refused a name that is not
// theirs in world, and then, with the masks raw sends back restored, a
// change of prev's runtime or of its agents' own tokens, and then the
// manifest's runtime must be one this server starts and its provider one
// this server has. prev is the stored row of the name, read only for a
// caller short of the apps area.
func (a *App) installRefused(w http.ResponseWriter, r *http.Request, raw []byte, mf manager.Manifest, world drafts.World, prev store.App) bool {
	if st := standingFrom(r.Context()); !st.Full {
		if ref := drafts.RegisterRefusal(world, draftStanding(st), mf.Metadata.Name); ref != nil {
			refuse(w, ref)
			return true
		}
		kept := keptManifest(raw, mf, prev.Manifest)
		refusal, err := serverAdminRuntimeRefusal(prev, kept, hiddenWrites(string(raw), prev.Manifest))
		if err == nil && refusal == "" {
			refusal, err = serverAdminAgentTokensRefusal(prev, kept)
		}
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "the change cannot be compared with the stored manifest of "+prev.Name+", so it is refused. Try again, and check the strazad log if it keeps failing.", err)
			return true
		}
		if refusal != "" {
			apiError(w, http.StatusForbidden, refusal)
			return true
		}
	}
	if err := a.manager.CheckRuntime(mf); err != nil {
		apiError(w, http.StatusUnprocessableEntity, err.Error())
		return true
	}
	if err := a.manager.CheckProvider(mf); err != nil {
		apiError(w, http.StatusUnprocessableEntity, err.Error())
		return true
	}
	return false
}

// handleAppsDelete removes an app by id or name, published as its removal:
// the row is soft-deleted, its access rows and every
// credential row go with it, the per-user OAuth grants included, its admin
// role and the roles it owned go with their memberships, and every record
// commits with the rows.
func (a *App) handleAppsDelete(w http.ResponseWriter, r *http.Request) {
	row, err := a.appByRef(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "unknown server")
		return
	}
	res, ok := a.publishOne(w, r, directRoute{
		prepare: func(drafts.World, drafts.Principal) (directChange, bool) {
			return directChange{Item: drafts.Item{Kind: drafts.KindApp, Name: row.Name, Op: drafts.OpRemove}}, true
		},
		// A server removed since appByRef read it answers as an unknown one.
		answer: func(f drafts.Finding) (int, string, bool) {
			return http.StatusNotFound, "unknown server", f.Code == "app.remove-missing"
		},
		bodyStatus: http.StatusBadRequest,
		failed:     func(error) string { return "remove failed" },
	})
	if !ok {
		return
	}
	id := row.ID
	if res.Item.ID != "" {
		id = res.Item.ID
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "removed"})
}

// toolPayload is one exposed MCP tool on the admin surface, so the IGA side
// reads real "what does this role grant" text. id is the app-qualified
// composite (app:tool), since
// bare tool names collide across apps; name stays bare with app alongside so
// consumers (the midPoint resource) keep composing display names their way.
type toolPayload struct {
	ID          string `json:"id"`
	App         string `json:"app"`
	AppID       string `json:"app_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// handleToolsList flattens the live manager catalog (in-memory views,
// exposure-filtered upstream inventory) into one tool list for IGA/admin
// consumers. Stopped apps hold no instance, so their tools are absent,
// matching what sessions can actually reach (fail-closed truth).
func (a *App) handleToolsList(w http.ResponseWriter, r *http.Request) {
	out := make([]toolPayload, 0, 64)
	for _, v := range a.manager.Views() {
		for _, t := range v.Tools {
			out = append(out, toolPayload{
				ID: v.Name + ":" + t.Name, App: v.Name, AppID: v.ID,
				Name: t.Name, Description: t.Description,
			})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAppRecheck triggers an immediate health probe for one app: the
// manager re-evaluates with the same semantics as the periodic loop and the
// refreshed view is returned. Stopped/unmanaged apps have nothing to probe.
func (a *App) handleAppRecheck(w http.ResponseWriter, r *http.Request) {
	row, err := a.appByRef(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "unknown server")
		return
	}
	v, ok := a.manager.HealthCheckOne(r.Context(), row.Name)
	if !ok {
		apiError(w, http.StatusConflict, fmt.Sprintf("the MCP server %s is stopped. Enable it with strazactl apps enable %s, then recheck.", row.Name, row.Name))
		return
	}
	a.emitEvent(r, "straza.audit.admin", map[string]any{
		"action": "apps.recheck", "app": row.Name, "status": v.Status, "detail": v.Detail,
	})
	p := appPayloadFromView(v)
	a.withAdminRole(r.Context(), &p, row, nil)
	writeJSON(w, http.StatusOK, p)
}

// handleAppEnable clears an app's admin pause and (re)starts it.
// Idempotent: enabling an already-running app just returns its current
// view. The refreshed view reflects the new status and paused is false on
// success. A corrupt stored manifest that can't be re-materialised surfaces
// the manager's actionable message as a 500.
func (a *App) handleAppEnable(w http.ResponseWriter, r *http.Request) {
	row, err := a.appByRef(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "unknown server")
		return
	}
	v, err := a.manager.Enable(r.Context(), row.Name)
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "unknown server")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, err.Error(), err)
		return
	}
	p := appPayloadFromView(v)
	p.Paused = a.manager.IsPaused(row.Name)
	a.withAdminRole(r.Context(), &p, row, nil)
	a.emitEvent(r, "straza.audit.admin", map[string]any{
		"action": "apps.enable", "app": row.Name, "status": v.Status, "adminRole": p.AdminRole,
	})
	writeJSON(w, http.StatusOK, p)
}

// handleAppDisable sets an app's admin pause and stops it.
// Idempotent: disabling an already-stopped app still records the pause and
// returns the stopped view. paused is true on success.
func (a *App) handleAppDisable(w http.ResponseWriter, r *http.Request) {
	row, err := a.appByRef(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "unknown server")
		return
	}
	v, err := a.manager.Disable(r.Context(), row.Name)
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "unknown server")
		return
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, err.Error(), err)
		return
	}
	p := appPayloadFromView(v)
	p.Paused = a.manager.IsPaused(row.Name)
	a.withAdminRole(r.Context(), &p, row, nil)
	a.emitEvent(r, "straza.audit.admin", map[string]any{
		"action": "apps.disable", "app": row.Name, "status": v.Status, "adminRole": p.AdminRole,
	})
	writeJSON(w, http.StatusOK, p)
}

// handleAppsLogs serves the runtime log ring.
func (a *App) handleAppsLogs(w http.ResponseWriter, r *http.Request) {
	row, err := a.appByRef(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "unknown server")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, ok := a.manager.LogEntries(row.Name, limit)
	if !ok {
		apiError(w, http.StatusConflict, "the server is not running (no log ring)")
		return
	}
	// lines stays for readers of the 0.93.0 shape; entries carries the same
	// lines with the receive time the console's gutter shows.
	lines := make([]string, len(entries))
	stamped := make([]logEntryPayload, len(entries))
	mask := serverLogMask(row.Manifest)
	for i, e := range entries {
		lines[i] = mask(e.Line)
		stamped[i] = logEntryPayload{At: e.At.UTC().Format(time.RFC3339Nano), Line: lines[i]}
	}
	writeJSON(w, http.StatusOK, map[string]any{"app": row.Name, "lines": lines, "entries": stamped})
}

// logEntryPayload is one runtime log line with its receive time (0.94.0).
type logEntryPayload struct {
	At   string `json:"t"`
	Line string `json:"line"`
}

// appByRef resolves the {id} path element as an app id or name.
func (a *App) appByRef(r *http.Request) (store.App, error) {
	ref := r.PathValue("id")
	row, err := a.store.Apps().GetByID(r.Context(), ref)
	if err == nil {
		return row, nil
	}
	return a.store.Apps().GetByName(r.Context(), ref)
}
