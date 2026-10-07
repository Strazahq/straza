package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/strazahq/straza/internal/store"
)

// pausedSettingsKey is the settings-KV key holding the admin-pause overlay: a
// JSON array of app names an operator has disabled. It reuses the existing
// settings aggregate, so no schema change.
const pausedSettingsKey = "manager.paused"

// IsPaused reports whether an app is under an admin pause. The boot
// loop and the starts of an apply consult it so a paused app is never
// resurrected from its persisted row.
func (m *Manager) IsPaused(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.paused[name]
}

// loadPaused restores the admin-pause overlay from the settings KV into memory
// (called at Boot, before any app starts). A missing key is an empty set; any
// other read error is returned so the caller can fail closed. A pause belongs
// to a live app, so a name with no live row is dropped: the trimmed set is
// stored first and memory follows, with one log line per dropped name. A name
// whose row read fails for another reason keeps its pause, and so does every
// name when the trimmed set cannot be stored.
func (m *Manager) loadPaused(ctx context.Context) error {
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	names, err := m.storedPaused(ctx)
	if err != nil {
		return err
	}
	var kept, dropped []string
	for _, n := range names {
		if _, err := m.opts.Store.Apps().GetByName(ctx, n); errors.Is(err, store.ErrNotFound) {
			dropped = append(dropped, n)
			continue
		}
		kept = append(kept, n)
	}
	if len(dropped) > 0 {
		if err := m.storePaused(ctx, kept); err != nil {
			m.opts.Log.Warn("manager: the pauses of servers that are not installed could not be dropped, so they stay until the next start", "err", err)
			kept = names
		} else {
			for _, n := range dropped {
				m.opts.Log.Info(fmt.Sprintf("manager: dropped the pause of %s, since no server with that name is installed", n))
			}
		}
	}
	m.mu.Lock()
	for _, n := range kept {
		m.paused[n] = true
	}
	m.mu.Unlock()
	return nil
}

// storedPaused reads the admin-pause overlay as the settings KV holds it. A
// missing key is an empty set, and any other failure is an error.
func (m *Manager) storedPaused(ctx context.Context) ([]string, error) {
	raw, err := m.opts.Store.Settings().Get(ctx, pausedSettingsKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("manager: load paused set: %w", err)
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil, fmt.Errorf("manager: decode paused set: %w", err)
	}
	return names, nil
}

// storePaused writes the admin-pause overlay to the settings KV as a sorted
// JSON array of app names. Callers hold adminMu.
func (m *Manager) storePaused(ctx context.Context, names []string) error {
	sorted := append([]string{}, names...)
	sort.Strings(sorted)
	b, err := json.Marshal(sorted)
	if err != nil {
		return err
	}
	return m.opts.Store.Settings().Set(ctx, pausedSettingsKey, string(b))
}

// setPausedLocked flips one app's admin-pause bit persist-first: it writes the
// new set to the settings KV and mutates the in-memory overlay only after the
// write succeeds, so a failed write leaves memory and store consistent and the
// caller must abort. A no-op transition (already in the desired state) neither
// writes nor errors. paused=true adds name; false removes it. The caller
// holds adminMu, which keeps two verbs from snapshotting the same set and
// losing each other's update.
func (m *Manager) setPausedLocked(ctx context.Context, name string, paused bool) error {
	m.mu.RLock()
	has := m.paused[name]
	names := make([]string, 0, len(m.paused)+1)
	for n := range m.paused {
		if !paused && n == name {
			continue
		}
		names = append(names, n)
	}
	m.mu.RUnlock()

	if paused == has {
		return nil // already in the desired state
	}
	if paused {
		names = append(names, name)
	}
	if err := m.storePaused(ctx, names); err != nil {
		return err
	}
	m.mu.Lock()
	if paused {
		m.paused[name] = true
	} else {
		delete(m.paused, name)
	}
	m.mu.Unlock()
	return nil
}

// Disable is the admin pause verb. It records an ADMIN-PAUSE overlay so
// neither boot nor an apply resurrects the app, then stops it if live. The
// pause is persisted FIRST: an app whose pause could not be recorded is
// never stopped, else a restart would silently bring it back. The
// stop keeps the row (status=stopped, row updated, straza.apps.removed
// emitted, catalog invalidated); pausing an already-stopped/de-adopted app is
// legal and idempotent. Returns the app's at-rest view (Paused=true).
func (m *Manager) Disable(ctx context.Context, name string) (AppView, error) {
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	if _, err := m.opts.Store.Apps().GetByName(ctx, name); err != nil {
		return AppView{}, err // store.ErrNotFound when absent
	}
	if err := m.setPausedLocked(ctx, name, true); err != nil {
		return AppView{}, err
	}
	if _, live := m.View(name); live {
		if err := m.stop(ctx, name); err != nil {
			return AppView{}, err
		}
	}
	row, err := m.opts.Store.Apps().GetByName(ctx, name)
	if err != nil {
		return AppView{}, err
	}
	return m.rowView(row), nil
}

// Enable is the admin resume verb. It clears the app's
// ADMIN-PAUSE overlay (persist-first, so a restart cannot re-pause a resumed
// app) and brings the app back exactly as a restart would. An app already live
// is left running (idempotent); otherwise the persisted manifest is re-read and
// the runtime restarted, mirroring the boot path; the deployed/health emit
// path fires naturally. A stored manifest that no longer parses is an
// actionable error. It writes the row's status only, so a manifest that a
// publish stored after Enable read the row stays.
func (m *Manager) Enable(ctx context.Context, name string) (AppView, error) {
	ctx = asked(ctx)
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	row, err := m.opts.Store.Apps().GetByName(ctx, name)
	if err != nil {
		return AppView{}, err
	}
	if err := m.setPausedLocked(ctx, name, false); err != nil {
		return AppView{}, err
	}
	if v, live := m.View(name); live {
		return v, nil // already running; idempotent
	}
	mf, err := FromJSON(row.Manifest)
	if err != nil {
		return AppView{}, fmt.Errorf("manager: enable %s: stored manifest unreadable (reinstall the app): %w", name, err)
	}
	if err := m.opts.Store.Apps().SetStatus(ctx, row.ID, StatusStarting); err != nil {
		return AppView{}, err
	}
	row.Status = StatusStarting
	if err := m.startInstance(ctx, row, mf, false); err != nil {
		return AppView{}, err
	}
	if v, ok := m.View(name); ok {
		return v, nil
	}
	return m.rowView(row), nil
}

// rowView builds an AppView from a persisted row for apps not currently in the
// live registry (e.g. a just-disabled app). Tools and health stamps are unknown
// at rest, so they stay zero; the manifest is decoded best-effort.
func (m *Manager) rowView(row store.App) AppView {
	av := AppView{
		ID: row.ID, Name: row.Name, Version: row.Version,
		Runtime: row.RuntimeKind, Status: row.Status, Source: row.Source,
		Paused: m.IsPaused(row.Name),
	}
	if mf, err := FromJSON(row.Manifest); err == nil {
		av.Manifest = mf
	}
	return av
}
