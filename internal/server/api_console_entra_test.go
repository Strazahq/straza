package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/strazahq/straza/internal/config"
)

// TestConfigEntraBlock pins that GET /v1/admin/config carries the
// identity.entra section as the config sets it, and no block while the
// section is off.
func TestConfigEntraBlock(t *testing.T) {
	t.Parallel()
	app, base := testApp(t)
	admin := seedIdentity(t, app)
	grantAdmin(t, app, admin.ID)
	bearer := loginDeviceFlow(t, base, "kim", "hunter2!")
	var got struct {
		Entra *configEntraPayload `json:"entra"`
	}
	if code := adminReq(t, "GET", base+"/v1/admin/config", bearer, nil, &got); code != http.StatusOK || got.Entra != nil {
		t.Fatalf("config with Entra off = %d, entra %+v, want no block", code, got.Entra)
	}
	app.cfg.Identity.Entra = config.Entra{
		Tenant: "d1d1d1d1-0000-4000-8000-000000000001", ClientID: "d2d2d2d2-0000-4000-8000-000000000002",
		RoleGroups: true, GroupNamePrefix: "Straza: ", DeltaInterval: time.Minute, ReconcileInterval: time.Hour,
		RemovalGuard: config.EntraRemovalGuard{Percent: 50, MinMembers: 5},
	}
	if code := adminReq(t, "GET", base+"/v1/admin/config", bearer, nil, &got); code != http.StatusOK || got.Entra == nil {
		t.Fatalf("config with Entra on = %d, entra %+v", code, got.Entra)
	}
	e := got.Entra
	if e.Tenant == "" || e.ClientID == "" || e.Read == nil || len(e.Read) != 0 || !e.RoleGroups || e.GroupNamePrefix != "Straza: " ||
		e.DeltaIntervalSeconds != 60 || e.ReconcileIntervalSeconds != 3600 || e.RemovalGuardPercent != 50 || e.RemovalGuardMinMembers != 5 {
		t.Errorf("entra block = %+v", e)
	}
}
