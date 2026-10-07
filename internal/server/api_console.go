package server

import (
	"errors"
	"net/http"

	"github.com/strazahq/straza/internal/config"
	"github.com/strazahq/straza/internal/policy"
	"github.com/strazahq/straza/internal/store"
)

// The console endpoints are purpose-built for the web console: one
// dashboard call and one redacted view of the effective security-layer
// configuration. Both are admin-gated control-plane reads.

type configEventsPayload struct {
	Embedded bool `json:"embedded"`
}

type configOIDCPayload struct {
	ExternalIssuer string `json:"external_issuer"`
	JITProvision   bool   `json:"jit_provision"`
}

type configSCIMPayload struct{}

type configGovernancePayload struct {
	MinAttestation         string `json:"min_attestation"`
	OfflineGraceTTLSeconds int    `json:"offline_grace_ttl_seconds"`
	LocalToolDefault       string `json:"local_tool_default"`
	AuditBackpressure      string `json:"audit_backpressure"`
}

type configAppsPayload struct {
	GitopsDirEnabled       bool `json:"gitops_dir_enabled"`
	UpstreamTimeoutSeconds int  `json:"upstream_timeout_seconds"`
}

// configApprovalPayload surfaces the effective socket hold for mode:approve
// gateway calls, so an operator who raised the hold finds the number in the
// console. Resolved, not raw: zero
// config renders the built-in default the gateway actually uses.
// UnsignedOwnDecisions is approval.unsignedOwnDecisions as configured.
type configApprovalPayload struct {
	GatewayHoldSeconds   int  `json:"gateway_hold_seconds"`
	UnsignedOwnDecisions bool `json:"unsigned_own_decisions"`
}

// configAdminPayload is the admin plane's block. SecondPerson is
// admin.secondPerson as configured, which no admin route writes.
type configAdminPayload struct {
	SecondPerson bool `json:"second_person"`
}

func approvalStatus(cfg *config.Config) configApprovalPayload {
	hold := cfg.Approval.GatewayHoldSeconds
	if hold == 0 {
		hold = int(gatewayHoldCap.Seconds())
	}
	return configApprovalPayload{GatewayHoldSeconds: hold, UnsignedOwnDecisions: cfg.Approval.UnsignedOwnDecisions}
}

// appsStatus reports the apps section. `watching` is whether this pod actually
// runs the GitOps directory watcher (server.go builds one only when a dir
// resolves), which is the fact the field claims. It is deliberately not
// `cfg.AppsDir() != ""`: that accessor falls back to "<dataDir>/apps", so it is
// ALWAYS true and the row would render "enabled" without looking at anything.
//
// The watcher is built on every boot, because AppsDir() cannot return empty
// and a dir that cannot be created aborts boot. So `false` is a shape this
// code reports correctly and no deployment produces, since the GitOps
// channel has no disable knob.
func appsStatus(cfg *config.Config, watching bool) configAppsPayload {
	upstream := int(cfg.Apps.UpstreamTimeout.Seconds())
	if upstream == 0 {
		upstream = 30 // the manager's built-in ceiling
	}
	return configAppsPayload{GitopsDirEnabled: watching, UpstreamTimeoutSeconds: upstream}
}

// configCapturePayload tells the operator whether conversation capture is in
// effect and under what bounds (the surveilled agent is told
// at checkin, so the operator must be told too; an empty Transcripts screen
// is ambiguous without it). PolicySets counts ACTIVE sets that opt sessions
// in; zero means nothing is captured no matter what the retention says.
type configCapturePayload struct {
	PolicySets     int    `json:"policy_sets"`
	Mode           string `json:"mode,omitempty"` // verbatim | redact | mixed
	RetentionHours int    `json:"retention_hours"`
	BodyStore      string `json:"body_store"` // inline | s3
}

// captureStatus derives the capture posture from the active PolicySets. A row
// that fails to parse is skipped, not fatal: it was validated at upload, and
// the config panel must not go dark over one bad row.
func captureStatus(sets []store.PolicySet, cfg *config.Config) configCapturePayload {
	out := configCapturePayload{
		RetentionHours: int(cfg.EffectiveCaptureRetention().Hours()),
		BodyStore:      "inline",
	}
	if cfg.Capture.BodyStore.Type != "" {
		out.BodyStore = cfg.Capture.BodyStore.Type
	}
	verbatim, redact := false, false
	for _, ps := range sets {
		if ps.Status != "active" {
			continue
		}
		doc, err := policy.Parse([]byte(ps.YAMLSource))
		if err != nil {
			continue
		}
		c := doc.Spec.Capture
		if c == nil || !c.Conversations {
			continue
		}
		out.PolicySets++
		if c.Mode == policy.CaptureModeRedact {
			redact = true
		} else {
			verbatim = true // unset mode captures verbatim (engine default)
		}
	}
	switch {
	case verbatim && redact:
		out.Mode = "mixed"
	case redact:
		out.Mode = policy.CaptureModeRedact
	case verbatim:
		out.Mode = policy.CaptureModeVerbatim
	}
	return out
}

type configPayload struct {
	Profile     string                  `json:"profile"`
	PublicURL   string                  `json:"public_url"`
	TLS         bool                    `json:"tls"`
	StoreDriver string                  `json:"store_driver"`
	Events      configEventsPayload     `json:"events"`
	OIDC        configOIDCPayload       `json:"oidc"`
	SCIM        configSCIMPayload       `json:"scim"`
	Governance  configGovernancePayload `json:"governance"`
	Approval    configApprovalPayload   `json:"approval"`
	Admin       configAdminPayload      `json:"admin"`
	Apps        configAppsPayload       `json:"apps"`
	Capture     configCapturePayload    `json:"capture"`
}

// handleConfigGet reports the effective security-layer configuration,
// redacted by construction: no DSNs, no file paths, no broker URLs;
// presence booleans stand in for anything connection-shaped. The knobs are
// file/env config and deliberately not mutable over the API. The capture
// section additionally reads the active PolicySets (a control-plane read,
// same as the overview, never a decision path).
func (a *App) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	cfg := a.cfg
	sets, err := a.store.Policies().List(r.Context())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "config: policies unavailable", err)
		return
	}
	minAtt := cfg.Governance.MinAttestation
	if minAtt == "" {
		minAtt = config.AttestationNone // effective value when unset
	}
	writeJSON(w, http.StatusOK, configPayload{
		Profile:     cfg.Profile,
		PublicURL:   cfg.Server.PublicURL,
		TLS:         cfg.TLSEnabled(),
		StoreDriver: cfg.Store.Driver,
		Events: configEventsPayload{
			Embedded: cfg.Events.Embedded,
		},
		OIDC: configOIDCPayload{
			ExternalIssuer: cfg.OIDC.Issuer, // discovery URLs are public by definition
			JITProvision:   cfg.OIDC.JITProvision,
		},
		SCIM: configSCIMPayload{},
		Governance: configGovernancePayload{
			MinAttestation:         minAtt,
			OfflineGraceTTLSeconds: int(cfg.Governance.OfflineGraceTTL.Seconds()),
			LocalToolDefault:       cfg.Governance.LocalToolDefault,
			AuditBackpressure:      cfg.Governance.AuditBackpressure,
		},
		Approval: approvalStatus(&cfg),
		Admin:    configAdminPayload{SecondPerson: cfg.Admin.SecondPerson},
		Apps:     appsStatus(&cfg, a.watcher != nil),
		Capture:  captureStatus(sets, &cfg),
	})
}

type countPayload struct {
	Total  int `json:"total"`
	Active int `json:"active"`
}

type appCountsPayload struct {
	Total    int `json:"total"`
	Running  int `json:"running"`
	Degraded int `json:"degraded"`
	Pending  int `json:"pending"`
	Stopped  int `json:"stopped"`
	Failed   int `json:"failed"`
}

type auditHeadPayload struct {
	HeadSeq  int64  `json:"head_seq"`
	HeadHash string `json:"head_hash"`
}

type overviewPayload struct {
	Profile    string           `json:"profile"`
	SnapshotID string           `json:"snapshot_id"`
	Users      countPayload     `json:"users"`
	Sessions   countPayload     `json:"sessions"`
	Apps       appCountsPayload `json:"apps"`
	Policies   countPayload     `json:"policies"`
	Audit      auditHeadPayload `json:"audit"`
	Denylist   struct {
		Entries int `json:"entries"`
	} `json:"denylist"`
	// Push is the fleet push-health surface: how many daemons hold a
	// live edge subscription to this pod (subscriber lists are per-pod soft
	// state), and whether this pod's lane is up at all. Active sessions
	// minus connected ≈ daemons riding the 30 s poll.
	Push struct {
		Connected int  `json:"connected"`
		LaneUp    bool `json:"lane_up"`
	} `json:"push"`
	Decisions decisionsPayload `json:"decisions"`
}

// handleOverviewGet assembles the console dashboard in one call. Full-table
// counts are fine at admin-dashboard scale and keep the store interface
// unchanged; revisit with dedicated count queries if an instance ever hosts
// enough rows to notice. window=hour adds the last hour's minutes to the
// decisions block, and any other window is refused with 400.
func (a *App) handleOverviewGet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	window := r.URL.Query().Get("window")
	if window != "" && window != overviewWindowHour {
		apiError(w, http.StatusBadRequest, overviewWindowMsg)
		return
	}
	out := overviewPayload{Profile: a.cfg.Profile, SnapshotID: a.snapshots.Current().ID}
	out.Denylist.Entries = a.denylist.size()
	if a.pushHub != nil {
		out.Push.LaneUp = true
		out.Push.Connected = a.pushHub.count()
	}

	users, err := a.store.Users().List(ctx)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "overview: users unavailable", err)
		return
	}
	out.Users.Total = len(users)
	for _, u := range users {
		if u.Status == store.UserActive {
			out.Users.Active++
		}
	}

	sessions, err := a.store.Sessions().List(ctx, "")
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "overview: sessions unavailable", err)
		return
	}
	out.Sessions.Total = len(sessions)
	for _, s := range sessions {
		if s.Status == store.SessionActive {
			out.Sessions.Active++
		}
	}

	apps, err := a.store.Apps().List(ctx)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "overview: MCP servers unavailable", err)
		return
	}
	out.Apps.Total = len(apps)
	for _, app := range apps {
		switch app.Status {
		case "running":
			out.Apps.Running++
		case "degraded":
			out.Apps.Degraded++
		case "pending", "starting":
			out.Apps.Pending++
		case "stopped":
			out.Apps.Stopped++
		case "failed":
			out.Apps.Failed++
		}
	}

	policies, err := a.store.Policies().List(ctx)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "overview: policies unavailable", err)
		return
	}
	out.Policies.Total = len(policies)
	for _, p := range policies {
		if p.Status == "active" {
			out.Policies.Active++
		}
	}

	head, err := a.store.Audit().Last(ctx)
	switch {
	case err == nil:
		out.Audit = auditHeadPayload{HeadSeq: head.Seq, HeadHash: head.Hash}
	case errors.Is(err, store.ErrNotFound):
		// empty chain: zero head
	default:
		a.fail(w, r, http.StatusInternalServerError, "overview: audit unavailable", err)
		return
	}

	// The decision block is counted here, inside the config-area overview
	// read, so a delegated admin holding config:read sees the dashboard
	// without any grant on the audit area. It reads the chain mirror, which
	// is a control-plane read like the counts above and never a decision
	// path.
	decisions, err := a.decisionsBlock(ctx)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "overview: decisions unavailable", err)
		return
	}
	out.Decisions = decisions
	if window == overviewWindowHour {
		if out.Decisions.Minutes, err = a.minutesBlock(ctx); err != nil {
			a.fail(w, r, http.StatusInternalServerError, "overview: decisions unavailable", err)
			return
		}
	}

	writeJSON(w, http.StatusOK, out)
}
