// Package tier1 embeds the published Tier-1 hook-profile conformance suite
// so `strazactl spec conformance` runs without a repo checkout.
package tier1

import "embed"

// FS holds cases.yaml (the case corpus) and policy.yaml (the PolicySet the
// implementation under test must enforce).
//
//go:embed cases.yaml policy.yaml
var FS embed.FS
