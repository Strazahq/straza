package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestAppsToolBindingsCredentials(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		app, err := s.Apps().Create(ctx, App{Name: "io.github/github", Version: "1.4.2", Manifest: `{"server":{}}`, RuntimeKind: "remote"})
		if err != nil {
			t.Fatalf("Apps.Create: %v", err)
		}
		if app.Status != "pending" || app.Source != "api" {
			t.Errorf("defaults not applied: %+v", app)
		}
		if _, err := s.Apps().Create(ctx, App{Name: "io.github/github", RuntimeKind: "remote"}); !errors.Is(err, ErrConflict) {
			t.Errorf("duplicate app name: want ErrConflict, got %v", err)
		}

		app.Status = "running"
		if _, err := s.Apps().Update(ctx, app); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Apps().GetByName(ctx, "io.github/github"); got.Status != "running" {
			t.Errorf("status = %q", got.Status)
		}

		role, _ := s.Roles().Create(ctx, Role{Name: "dev"})
		b, err := s.ToolBindings().Create(ctx, ToolBinding{RoleID: role.ID, AppID: app.ID, ToolMatcher: `["get_*","list_*"]`})
		if err != nil {
			t.Fatalf("ToolBindings.Create: %v", err)
		}
		byRole, err := s.ToolBindings().ListByRole(ctx, role.ID)
		if err != nil || len(byRole) != 1 {
			t.Fatalf("ListByRole = %+v, %v", byRole, err)
		}
		// JSONB normalizes whitespace: compare parsed values, not bytes.
		var globs []string
		if err := json.Unmarshal([]byte(byRole[0].ToolMatcher), &globs); err != nil || len(globs) != 2 || globs[0] != "get_*" || globs[1] != "list_*" {
			t.Fatalf("tool_matcher round-trip = %q, %v", byRole[0].ToolMatcher, err)
		}

		c, err := s.Credentials().Create(ctx, Credential{
			AppID: app.ID, Scope: "role", OwnerID: role.ID, Kind: "static",
			EncPayload: []byte{0x01, 0x02, 0x03},
		})
		if err != nil {
			t.Fatalf("Credentials.Create: %v", err)
		}
		got, err := s.Credentials().GetByID(ctx, c.ID)
		if err != nil || len(got.EncPayload) != 3 || got.EncPayload[2] != 0x03 {
			t.Fatalf("ciphertext round-trip: %+v, %v", got, err)
		}
		got.EncPayload = []byte{0xFF}
		upd, err := s.Credentials().Update(ctx, got)
		if err != nil || upd.RotatedAt == nil || len(upd.EncPayload) != 1 {
			t.Fatalf("Update = %+v, %v", upd, err)
		}
		if list, _ := s.Credentials().ListByApp(ctx, app.ID); len(list) != 1 {
			t.Errorf("ListByApp = %d", len(list))
		}
		if err := s.Credentials().Delete(ctx, c.ID); err != nil {
			t.Fatal(err)
		}

		if err := s.ToolBindings().Delete(ctx, b.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Apps().SoftDelete(ctx, app.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Apps().GetByID(ctx, app.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("soft-deleted app still visible: %v", err)
		}
	})
}

// TestCredentialsListByOwner pins the by-owner read the SCIM deprovisioning
// wipe walks: one user's grants across apps, none for a stranger, and a
// role-scoped row of the same owner id stays out of the user scope.
func TestCredentialsListByOwner(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		app1, err := s.Apps().Create(ctx, App{Name: "github", RuntimeKind: "remote", Manifest: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		app2, err := s.Apps().Create(ctx, App{Name: "jira", RuntimeKind: "remote", Manifest: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		const owner = "user-1"
		for _, c := range []Credential{
			{AppID: app1.ID, Scope: CredScopeUser, OwnerID: owner, Kind: CredOAuth, EncPayload: []byte{1}},
			{AppID: app2.ID, Scope: CredScopeUser, OwnerID: owner, Kind: CredOAuth, EncPayload: []byte{2}},
			{AppID: app1.ID, Scope: CredScopeRole, OwnerID: owner, Kind: CredStatic, EncPayload: []byte{3}},
			{AppID: app1.ID, Scope: CredScopeUser, OwnerID: "user-2", Kind: CredOAuth, EncPayload: []byte{4}},
		} {
			if _, err := s.Credentials().Create(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
		cases := []struct {
			name     string
			scope    string
			owner    string
			wantApps map[string]bool
		}{
			{"user with two grants on two apps", CredScopeUser, owner, map[string]bool{app1.ID: true, app2.ID: true}},
			{"user with no rows", CredScopeUser, "user-3", map[string]bool{}},
			{"role scope of the same owner id", CredScopeRole, owner, map[string]bool{app1.ID: true}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				rows, err := s.Credentials().ListByOwner(ctx, tc.scope, tc.owner)
				if err != nil {
					t.Fatal(err)
				}
				got := map[string]bool{}
				for _, r := range rows {
					if r.Scope != tc.scope || r.OwnerID != tc.owner {
						t.Errorf("row %s = scope %q owner %q, want %q %q", r.ID, r.Scope, r.OwnerID, tc.scope, tc.owner)
					}
					got[r.AppID] = true
				}
				if len(rows) != len(tc.wantApps) || len(got) != len(tc.wantApps) {
					t.Fatalf("ListByOwner = %d rows on apps %v, want apps %v", len(rows), got, tc.wantApps)
				}
				for id := range tc.wantApps {
					if !got[id] {
						t.Errorf("app %s missing from %v", id, got)
					}
				}
			})
		}
	})
}

func TestPoliciesAndSnapshots(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		p, err := s.Policies().Create(ctx, PolicySet{Name: "base", Priority: 10, YAMLSource: "apiVersion: straza.dev/v1beta1"})
		if err != nil {
			t.Fatalf("Policies.Create: %v", err)
		}
		if p.Status != "draft" {
			t.Errorf("default status = %q", p.Status)
		}
		p.Status = "active"
		p.CompiledHash = "h1"
		if _, err := s.Policies().Update(ctx, p); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Policies().GetByName(ctx, "base"); got.CompiledHash != "h1" {
			t.Errorf("compiled_hash = %q", got.CompiledHash)
		}
		if list, _ := s.Policies().List(ctx); len(list) != 1 {
			t.Errorf("List = %d", len(list))
		}

		sn1, err := s.Snapshots().Create(ctx, Snapshot{ID: "hash-1", SignerKeyID: "k1", Blob: []byte("cbor1")})
		if err != nil {
			t.Fatalf("Snapshots.Create: %v", err)
		}
		if sn1.Size != 5 {
			t.Errorf("size = %d, want 5", sn1.Size)
		}
		if _, err := s.Snapshots().Create(ctx, Snapshot{ID: "hash-2", SignerKeyID: "k1", Blob: []byte("cbor22")}); err != nil {
			t.Fatal(err)
		}

		if _, err := s.Snapshots().GetActive(ctx); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetActive with none active: want ErrNotFound, got %v", err)
		}
		if err := s.Snapshots().SetActive(ctx, "hash-1"); err != nil {
			t.Fatal(err)
		}
		if err := s.Snapshots().SetActive(ctx, "hash-2"); err != nil {
			t.Fatal(err)
		}
		active, err := s.Snapshots().GetActive(ctx)
		if err != nil || active.ID != "hash-2" {
			t.Fatalf("GetActive = %+v, %v (exactly one snapshot must be active)", active, err)
		}
		if err := s.Snapshots().SetActive(ctx, "no-such"); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetActive(missing) = %v, want ErrNotFound", err)
		}
		// A failed SetActive must not have deactivated the current one.
		if active, _ := s.Snapshots().GetActive(ctx); active.ID != "hash-2" {
			t.Errorf("failed SetActive clobbered active snapshot: %+v", active)
		}
		if err := s.Policies().Delete(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOutboxAndAudit(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		e1, err := s.Outbox().Insert(ctx, OutboxEvent{Subject: "straza.identity.created", CE: `{"id":"1"}`})
		if err != nil {
			t.Fatalf("Outbox.Insert: %v", err)
		}
		e2, _ := s.Outbox().Insert(ctx, OutboxEvent{Subject: "straza.identity.updated", CE: `{"id":"2"}`})

		pending, err := s.Outbox().ListUnpublished(ctx, 10)
		if err != nil || len(pending) != 2 {
			t.Fatalf("ListUnpublished = %d, %v", len(pending), err)
		}
		if pending[0].ID != e1.ID {
			t.Errorf("outbox order: got %s first", pending[0].ID)
		}
		if err := s.Outbox().IncAttempts(ctx, e1.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.Outbox().MarkPublished(ctx, []string{e1.ID, e2.ID}); err != nil {
			t.Fatal(err)
		}
		if pending, _ := s.Outbox().ListUnpublished(ctx, 10); len(pending) != 0 {
			t.Errorf("still unpublished: %v", pending)
		}
		if err := s.Outbox().MarkPublished(ctx, nil); err != nil {
			t.Errorf("MarkPublished(nil) = %v", err)
		}

		if _, err := s.Audit().Last(ctx); !errors.Is(err, ErrNotFound) {
			t.Errorf("Audit.Last on empty log: want ErrNotFound, got %v", err)
		}
		r1, err := s.Audit().Append(ctx, "ce-1", `{"e":1}`, "", "h1")
		if err != nil {
			t.Fatalf("Audit.Append: %v", err)
		}
		r2, _ := s.Audit().Append(ctx, "ce-2", `{"e":2}`, "h1", "h2")
		if r2.Seq <= r1.Seq {
			t.Errorf("audit seq not monotonic: %d then %d", r1.Seq, r2.Seq)
		}
		// ce_id dedupe: a second append of the same CloudEvent id conflicts.
		if _, err := s.Audit().Append(ctx, "ce-1", `{"e":1}`, "h2", "h3"); !errors.Is(err, ErrConflict) {
			t.Errorf("duplicate ce_id: want ErrConflict, got %v", err)
		}
		if exists, _ := s.Audit().ExistsCE(ctx, "ce-1"); !exists {
			t.Error("ExistsCE(ce-1) should be true")
		}
		if exists, _ := s.Audit().ExistsCE(ctx, "ce-nope"); exists {
			t.Error("ExistsCE(ce-nope) should be false")
		}
		last, err := s.Audit().Last(ctx)
		if err != nil || last.Hash != "h2" || last.PrevHash != "h1" {
			t.Fatalf("Last = %+v, %v", last, err)
		}
		list, err := s.Audit().List(ctx, r1.Seq, 10)
		if err != nil || len(list) != 1 || list[0].Seq != r2.Seq {
			t.Fatalf("List(after r1) = %+v, %v", list, err)
		}
	})
}

func TestRevocationsAndSigningKeys(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		rv, err := s.Revocations().Create(ctx, Revocation{Kind: RevokeUser, TargetID: "u1", Reason: "offboarded"})
		if err != nil {
			t.Fatalf("Revocations.Create: %v", err)
		}
		if _, err := s.Revocations().Create(ctx, Revocation{Kind: RevokeSession, TargetID: "s1"}); err != nil {
			t.Fatal(err)
		}
		all, err := s.Revocations().List(ctx)
		if err != nil || len(all) != 2 {
			t.Fatalf("List = %d, %v", len(all), err)
		}
		since, err := s.Revocations().ListSince(ctx, rv.CreatedAt)
		if err != nil || len(since) != 2 {
			t.Fatalf("ListSince(rv.CreatedAt) = %d, %v", len(since), err)
		}
		if since, _ := s.Revocations().ListSince(ctx, now().Add(time.Hour)); len(since) != 0 {
			t.Errorf("ListSince(future) = %d, want 0", len(since))
		}

		k, err := s.SigningKeys().Create(ctx, SigningKey{Purpose: KeyPurposeSession, PrivateKey: []byte("priv"), PublicKey: []byte("pub")})
		if err != nil {
			t.Fatalf("SigningKeys.Create: %v", err)
		}
		if k.Status != KeyActive {
			t.Errorf("default status = %q", k.Status)
		}
		if _, err := s.SigningKeys().Create(ctx, SigningKey{Purpose: KeyPurposeSnapshot, PrivateKey: []byte("p2"), PublicKey: []byte("q2")}); err != nil {
			t.Fatal(err)
		}
		sessionKeys, err := s.SigningKeys().ListByPurpose(ctx, KeyPurposeSession)
		if err != nil || len(sessionKeys) != 1 || string(sessionKeys[0].PublicKey) != "pub" {
			t.Fatalf("ListByPurpose = %+v, %v", sessionKeys, err)
		}
		if err := s.SigningKeys().SetStatus(ctx, k.KID, KeyRetiring); err != nil {
			t.Fatal(err)
		}
		got, _ := s.SigningKeys().Get(ctx, k.KID)
		if got.Status != KeyRetiring || got.RotatedAt == nil {
			t.Errorf("after SetStatus: %+v", got)
		}
	})
}

func TestSettings(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if err := s.Settings().Set(ctx, "issuer", "builtin"); err != nil {
			t.Fatal(err)
		}
		if err := s.Settings().Set(ctx, "issuer", "external"); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		v, err := s.Settings().Get(ctx, "issuer")
		if err != nil || v != "external" {
			t.Fatalf("Get = %q, %v", v, err)
		}
		if err := s.Settings().Set(ctx, "name", "straza"); err != nil {
			t.Fatal(err)
		}
		all, err := s.Settings().List(ctx)
		if err != nil || len(all) != 2 {
			t.Fatalf("List = %v, %v", all, err)
		}
		if err := s.Settings().Delete(ctx, "issuer"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Settings().Get(ctx, "issuer"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
		// SetIfAbsent: first writer wins, later calls never overwrite.
		if err := s.Settings().SetIfAbsent(ctx, "project.id", "prj_first"); err != nil {
			t.Fatalf("SetIfAbsent insert: %v", err)
		}
		if err := s.Settings().SetIfAbsent(ctx, "project.id", "prj_second"); err != nil {
			t.Fatalf("SetIfAbsent conflict must be a no-op, got %v", err)
		}
		if v, err := s.Settings().Get(ctx, "project.id"); err != nil || v != "prj_first" {
			t.Fatalf("SetIfAbsent overwrote: Get = %q, %v (want prj_first)", v, err)
		}
		if err := s.Settings().Delete(ctx, "issuer"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("double delete: want ErrNotFound, got %v", err)
		}
		// Update rewrites a live row and leaves a deleted key deleted, so a
		// writer that lost a race with Delete cannot bring the row back.
		if err := s.Settings().Update(ctx, "name", "straza-2"); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if v, err := s.Settings().Get(ctx, "name"); err != nil || v != "straza-2" {
			t.Fatalf("after Update: Get = %q, %v", v, err)
		}
		if err := s.Settings().Update(ctx, "issuer", "revived"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Update of a deleted key: want ErrNotFound, got %v", err)
		}
		if _, err := s.Settings().Get(ctx, "issuer"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Update revived a deleted key: %v", err)
		}
	})
}
