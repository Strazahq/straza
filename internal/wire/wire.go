// Package wire holds the JSON status shapes shared by the strazad handlers
// and the strazactl client (/readyz and /version bodies) so the CLI decodes
// them without linking internal/server.
package wire

import "github.com/strazahq/straza/internal/version"

// ReadyStatus is the /readyz response body.
type ReadyStatus struct {
	Status     string            `json:"status"` // ok|degraded
	Components map[string]string `json:"components"`
}

// VersionStatus is the /version response body.
type VersionStatus struct {
	version.Info
	Profile string `json:"profile"`
	// Runtimes lists the app runtimes this host can run: remote and
	// command always, oci only when docker was found on PATH at boot.
	Runtimes []string `json:"runtimes,omitempty"`
	// Approver is present only when strazad itself serves TLS for the
	// approver surface (dedicated listener or native main TLS); it names
	// the pinned surface so `straza doctor` can report it without
	// server-side filesystem access.
	Approver *ApproverVersion `json:"approver,omitempty"`
}

// ApproverVersion is the /version approver block. Everything here is public
// by construction: the URL and pin ride the enroll QR, and the expiry is
// visible in the TLS handshake. CertFile is included only for the
// auto-minted pair ("where does my zero-config state live" is the question
// it answers); operator-provided paths stay out of an unauthenticated body.
type ApproverVersion struct {
	PublicURL    string `json:"public_url"`
	TLSSPKIPin   string `json:"tls_spki_pin"`
	CertNotAfter string `json:"cert_not_after,omitempty"` // RFC 3339
	AutoMinted   bool   `json:"auto_minted"`
	CertFile     string `json:"cert_file,omitempty"`
}
