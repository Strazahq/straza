// Package adapters embeds the harness dialect mapping specs. Adapters are
// data, not per-harness code forks: each YAML maps one harness's
// hook payloads onto the canonical event model (spec/hook-profile). The
// published spec-side copies live under spec/hook-profile/mappings/.
package adapters

import "embed"

// FS holds one <dialect>.yaml per supported harness.
//
//go:embed claude-code.yaml codex.yaml gemini.yaml python-sdk.yaml
var FS embed.FS
