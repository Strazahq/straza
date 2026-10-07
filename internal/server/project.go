package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/strazahq/straza/internal/store"
)

// Project identity: every deployment gets a stable opaque id the approver app
// keys enrollments on (multi-backend support: one phone, several strazads),
// plus a human display name. The id is generated once at first boot and
// persisted in the settings KV; the name is config (server.projectName) or a
// deterministic "straza-<4hex>" derived from the id. Names are labels, not
// keys: they may collide across deployments by design; clients MUST key on
// the id.

const projectIDSettingsKey = "server.projectId"

// maxProjectNameLen bounds the display name (QR payloads have a byte budget).
const maxProjectNameLen = 64

// resolveProjectID returns the deployment's persistent project id, creating
// it on first boot. Multi-pod safe: SetIfAbsent never overwrites, and every
// pod adopts whatever value won the insert.
func resolveProjectID(ctx context.Context, st store.Store) (string, error) {
	settings := st.Settings()
	id, err := settings.Get(ctx, projectIDSettingsKey)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("read project id: %w", err)
	}
	u, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate project id: %w", err)
	}
	if err := settings.SetIfAbsent(ctx, projectIDSettingsKey, "prj_"+u.String()); err != nil {
		return "", fmt.Errorf("persist project id: %w", err)
	}
	id, err = settings.Get(ctx, projectIDSettingsKey)
	if err != nil {
		return "", fmt.Errorf("re-read project id: %w", err)
	}
	return id, nil
}

// projectName resolves the display name: sanitized config value when set,
// else "straza-" + the last 4 hex characters of the id's random tail,
// deterministic from the persisted id, so it never changes across restarts
// and needs no second settings row.
func (a *App) projectName() string {
	if name := sanitizeProjectName(a.cfg.Server.ProjectName); name != "" {
		return name
	}
	hexed := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(a.projectID, "prj_"), "-", ""))
	if len(hexed) < 4 {
		return "straza"
	}
	return "straza-" + hexed[len(hexed)-4:]
}

// projectRef is the {id,name} pair embedded in enroll responses and the
// enroll QR payload, so one phone can enroll with several deployments.
type projectRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (a *App) projectRef() projectRef {
	return projectRef{ID: a.projectID, Name: a.projectName()}
}

// sanitizeProjectName trims, strips control characters, and caps the label at
// maxProjectNameLen runes. Display-only, so lenient by design.
func sanitizeProjectName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	runes := []rune(s)
	if len(runes) > maxProjectNameLen {
		s = string(runes[:maxProjectNameLen])
	}
	return strings.TrimSpace(s)
}
