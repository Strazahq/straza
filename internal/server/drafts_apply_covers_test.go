package server

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/strazahq/straza/internal/store"
)

// TestApplySwapsTheCoversLine pins the swap half of a publish that changes
// only a role's covers line: the publish replaces the access row, and the
// apply drops the old row and leaves the gateway's table holding the new
// one with its covers line. A control-plane refresh reads the same table.
func TestApplySwapsTheCoversLine(t *testing.T) {
	t.Parallel()
	app, _ := testApp(t)
	ctx := context.Background()
	up := startGatewayUpstream(t)
	dev, err := app.store.Roles().Create(ctx, store.Role{Name: "dev", Kind: store.RoleKindApplication})
	if err != nil {
		t.Fatal(err)
	}
	echo := putServer(t, app, echoManifest(up.URL))
	every := &store.ObjectScope{Every: true}
	old, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: dev.ID, AppID: echo.ID, ToolMatcher: `["*"]`, Scope: every})
	if err != nil {
		t.Fatal(err)
	}
	if err := publishOn(ctx, app, nil); err != nil {
		t.Fatal(err)
	}
	assertDevRow(t, app, "after the first apply", old.ID, every)

	pay := &store.ObjectScope{Objects: []store.CoveredObject{{ID: "11", Value: "org/pay-api"}}}
	if err := app.store.ToolBindings().Delete(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	row, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: dev.ID, AppID: echo.ID, ToolMatcher: `["*"]`, Scope: pay})
	if err != nil {
		t.Fatal(err)
	}
	if err := publishOn(ctx, app, nil); err != nil {
		t.Fatal(err)
	}
	assertDevRow(t, app, "after the covers change", row.ID, pay)
	if err := app.refreshBindings(ctx); err != nil {
		t.Fatal(err)
	}
	assertDevRow(t, app, "after a refresh", row.ID, pay)
}

// assertDevRow checks that the gateway's table holds one row of the role
// dev, with id and the covers line scope.
func assertDevRow(t *testing.T, app *App, when, id string, scope *store.ObjectScope) {
	t.Helper()
	var rows []gwBinding
	for _, b := range app.gateway.bindings.Load().([]gwBinding) {
		if b.Role == "dev" {
			rows = append(rows, b)
		}
	}
	if len(rows) != 1 || rows[0].ID != id || !reflect.DeepEqual(rows[0].Scope, scope) {
		t.Errorf("%s the gateway holds the rows %+v of dev, want only %s covering %+v", when, rows, id, scope)
	}
}

// TestUnreadableCoversLineGrantsNothing pins an access row whose stored
// covers line cannot be read, such as {}: the apply and a control-plane
// refresh both leave that row out of the gateway's table, keep every other
// row, and log the row's sentence once per read instead of failing.
func TestUnreadableCoversLineGrantsNothing(t *testing.T) {
	t.Parallel()
	var logs *syncBuffer
	app, _ := testAppPreRun(t, []func(*App){func(a *App) { a.log, logs = captureLogger() }})
	ctx := context.Background()
	up := startGatewayUpstream(t)
	echo := putServer(t, app, echoManifest(up.URL))
	every := &store.ObjectScope{Every: true}
	var rows []store.ToolBinding
	for _, name := range []string{"dev", "ops"} {
		ro, err := app.store.Roles().Create(ctx, store.Role{Name: name, Kind: store.RoleKindApplication})
		if err != nil {
			t.Fatal(err)
		}
		b, err := app.store.ToolBindings().Create(ctx, store.ToolBinding{RoleID: ro.ID, AppID: echo.ID, ToolMatcher: `["*"]`, Scope: every})
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, b)
	}
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(filepath.ToSlash(app.cfg.Store.DSN))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`UPDATE tool_bindings SET object_scope = '{}' WHERE id = ?`, rows[1].ID); err != nil {
		t.Fatal(err)
	}

	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"the apply", func() error { return publishOn(ctx, app, nil) }},
		{"a refresh", func() error { return app.refreshBindings(ctx) }},
	} {
		logs.Reset()
		if err := step.run(); err != nil {
			t.Fatalf("%s with an unreadable covers line: %v", step.name, err)
		}
		assertDevRow(t, app, "after "+step.name, rows[0].ID, every)
		for _, b := range app.gateway.bindings.Load().([]gwBinding) {
			if b.Role == "ops" {
				t.Errorf("after %s the gateway holds the unreadable row %+v", step.name, b)
			}
		}
		if n := strings.Count(logs.String(), rows[1].ID); n != 1 {
			t.Errorf("%s logged the unreadable row %d times, want once:\n%s", step.name, n, logs.String())
		}
	}
}
