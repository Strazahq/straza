package manager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/strazahq/straza/internal/store"
)

// reconcileStartTimeout bounds each start inside StartMissing. A remote
// server is probed before its start returns, and StartMissing runs on the
// replica's converge consumer, so a server that never answers would
// otherwise hold back every policy and identity change queued behind it.
const reconcileStartTimeout = 20 * time.Second

// errNoReadTime refuses a zero since, which is younger than no instance and
// would leave every stale server running.
var errNoReadTime = errors.New("manager: no server was judged or stopped, because the caller gave no time for its read of the rows. " +
	"It is a defect in strazad: report it with this line")

// Stale names the servers whose instance on this replica no longer
// matches rows: the row is gone, stopped or paused, its manifest changed,
// or it was parked for a credential that arrived. It reads the pause set.
// An instance registered at or after since is left alone, because a
// write after rows were read made it. A zero since is refused. Stale only
// judges, so it does not wait for adminMu, which a start holds across its
// probe while the caller holds its config lock: StopNamed looks again under
// adminMu before it stops, and StartMissing makes the pause set this
// replica's.
func (m *Manager) Stale(ctx context.Context, rows []store.App, since time.Time) ([]string, error) {
	if since.IsZero() {
		return nil, errNoReadTime
	}
	stored, err := m.storedPaused(ctx)
	if err != nil {
		return nil, err
	}
	paused := make(map[string]bool, len(stored))
	for _, n := range stored {
		paused[n] = true
	}
	byName := make(map[string]store.App, len(rows))
	for _, row := range rows {
		byName[row.Name] = row
	}
	var stale []string
	for _, inst := range m.instances() {
		if !inst.registered.Before(since) {
			continue
		}
		if row, ok := byName[inst.app.Name]; ok && m.runs(inst, row, paused) {
			continue
		}
		stale = append(stale, inst.app.Name)
	}
	return stale, nil
}

// runs reports whether inst still serves row as it stands: the same server,
// neither stopped nor paused, the same manifest, and not parked for a
// credential that arrived since.
func (m *Manager) runs(inst *instance, row store.App, paused map[string]bool) bool {
	return row.Status != StatusStopped && !paused[row.Name] && inst.app.ID == row.ID &&
		sameManifest(inst.app.Manifest, row.Manifest) && !m.credentialArrived(inst)
}

// StopNamed stops the instances of the named servers on this replica that
// are still stale, writing no row and emitting no event. Under adminMu it
// looks again, because an admin verb may have run since Stale released the
// lock: an instance registered at or after since is left alone, as Stale
// leaves it, and so is one whose row, read again with the pause set,
// matches it once more. A failed read counts as a row that still differs,
// so a stale server stops. A name with no instance is skipped, the catalog
// subscribers hear once when any instance stopped, and a zero since is
// refused.
func (m *Manager) StopNamed(ctx context.Context, names []string, since time.Time) error {
	if since.IsZero() {
		return errNoReadTime
	}
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	paused, pausedErr := m.readPaused(ctx)
	stopped := false
	for _, name := range names {
		m.mu.RLock()
		inst := m.byName[name]
		m.mu.RUnlock()
		if inst == nil || !inst.registered.Before(since) {
			continue
		}
		if pausedErr == nil {
			if row, err := m.opts.Store.Apps().GetByName(ctx, name); err == nil && m.runs(inst, row, paused) {
				continue
			}
		}
		m.stopInstance(name)
		stopped = true
	}
	if stopped {
		m.fireChange()
	}
	return nil
}

// Log lines of StartMissing for a server it does not announce, each logged
// with its cause. An announced server's answer is the line and the cause,
// but for a failed start, whose error it answers as Install does.
const (
	startFailed = "manager: a server changed on another replica did not start on this one. " +
		"Its health reason says why, and a restart of this replica tries again"
	manifestUnreadable = "manager: the stored manifest cannot be read, so this replica runs no instance of the server. " +
		"Install the server again to replace the manifest"
	rereadFailed = "manager: the row or the pause set of a server could not be read again before its start, so this replica did not start it. " +
		"It tries again at the next server change made on another replica, and a restart of this replica loads every server from the store"
)

// StartMissing starts every row with no instance on this replica unless
// it is stopped or paused, each start bounded by 20 seconds. Under
// adminMu it reads the row and the pause set again before each start, and
// skips a row that is gone, stopped, paused or carries another manifest,
// because the apply that moved it starts it. A server in announce emits
// straza.apps.deployed once it runs and has its start error in the
// answer. Every other start is logged. The catalog subscribers hear once
// when any start ran. It first makes the stored pause set this replica's,
// also when nothing starts, because an admin verb stores the set it builds
// from this replica's copy.
func (m *Manager) StartMissing(ctx context.Context, rows []store.App, announce map[string]bool) map[string]error {
	m.adminMu.Lock()
	defer m.adminMu.Unlock()
	if _, err := m.readPaused(ctx); err != nil {
		m.opts.Log.Warn("manager: the pause set could not be read, so this replica keeps the copy it had until the next change", "err", err)
	}
	var errs map[string]error
	fail := func(name, line string, cause, answer error) {
		if !announce[name] {
			m.opts.Log.Error(line, "app", name, "err", cause)
			return
		}
		if errs == nil {
			errs = map[string]error{}
		}
		errs[name] = answer
	}
	started := false
	for _, row := range rows {
		m.mu.RLock()
		_, live := m.byName[row.Name]
		m.mu.RUnlock()
		if live || row.Status == StatusStopped {
			continue
		}
		paused, err := m.readPaused(ctx)
		if err != nil {
			fail(row.Name, rereadFailed, err, fmt.Errorf("%s: %w", rereadFailed, err))
			continue
		}
		cur, err := m.opts.Store.Apps().GetByName(ctx, row.Name)
		switch {
		case errors.Is(err, store.ErrNotFound):
			continue
		case err != nil:
			fail(row.Name, rereadFailed, err, fmt.Errorf("%s: %w", rereadFailed, err))
			continue
		case cur.ID != row.ID || cur.Status == StatusStopped || paused[cur.Name] || !sameManifest(cur.Manifest, row.Manifest):
			continue
		}
		mf, err := FromJSON(cur.Manifest)
		if err != nil {
			fail(row.Name, manifestUnreadable, err, fmt.Errorf("%s: %w", manifestUnreadable, err))
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, reconcileStartTimeout)
		err = m.startInstance(sctx, cur, mf, !announce[row.Name])
		cancel()
		started = true
		if err != nil {
			fail(row.Name, startFailed, err, err)
		}
	}
	if started {
		m.fireChange()
	}
	return errs
}

// restartParked starts a server that parked for want of its credential
// again, from its row as the store holds it now, read again with the pause
// set. was, the parked instance's copy of the row, only names the row and
// its manifest. A row that went, stopped, paused or carries another
// manifest keeps the parked instance for the apply that judges it, since
// this replica may not have applied that row's policy yet. The caller holds
// adminMu.
func (m *Manager) restartParked(ctx context.Context, was store.App) {
	paused, err := m.readPaused(ctx)
	if err != nil {
		m.opts.Log.Error(rereadFailed, "app", was.Name, "err", err)
		return
	}
	row, err := m.opts.Store.Apps().GetByName(ctx, was.Name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return
	case err != nil:
		m.opts.Log.Error(rereadFailed, "app", was.Name, "err", err)
		return
	case row.ID != was.ID || row.Status == StatusStopped || paused[row.Name] || !sameManifest(row.Manifest, was.Manifest):
		return
	}
	mf, err := FromJSON(row.Manifest)
	if err != nil {
		m.opts.Log.Error(manifestUnreadable, "app", row.Name, "err", err)
		return
	}
	if err := m.startInstance(ctx, row, mf, true); err != nil {
		m.opts.Log.Error("manager: restart after secret", "app", row.Name, "err", err)
	}
}

// sameManifest reports whether two stored manifests are one, whatever key
// order and spacing the store rendered each in.
func sameManifest(a, b string) bool {
	if a == b {
		return true
	}
	ka, errA := manifestKey(a)
	kb, errB := manifestKey(b)
	return errA == nil && errB == nil && ka == kb
}

// readPaused reads the pause set as the settings hold it and makes it this
// replica's, so that a pause or a resume recorded through another replica
// holds here too. The caller holds adminMu, under which nothing else writes
// the set, so the answer reads without mu.
func (m *Manager) readPaused(ctx context.Context) (map[string]bool, error) {
	names, err := m.storedPaused(ctx)
	if err != nil {
		return nil, err
	}
	paused := make(map[string]bool, len(names))
	for _, n := range names {
		paused[n] = true
	}
	m.mu.Lock()
	m.paused = paused
	m.mu.Unlock()
	return paused, nil
}

// credentialArrived reports whether a command or oci instance parked for want
// of its credential could start now, because the broker holds the secret its
// manifest injects. SecretUpdated starts such an instance on the replica
// that took the secret. Asking the broker keeps a server whose secret is
// still missing from restarting at every reconcile.
func (m *Manager) credentialArrived(inst *instance) bool {
	kind := inst.manifest.Straza.Runtime.Kind
	inst.mu.Lock()
	parked := inst.status == StatusPending
	inst.mu.Unlock()
	if !parked || (kind != RuntimeCommand && kind != RuntimeOCI) {
		return false
	}
	_, ok := m.commandEnvSecret(inst)
	return ok
}

// manifestKey decodes a stored manifest and encodes it again, so two copies
// of one manifest compare equal whatever key order and spacing the store
// rendered them in, as Postgres renders JSONB in its own.
func manifestKey(raw string) (string, error) {
	mf, err := FromJSON(raw)
	if err != nil {
		return "", err
	}
	return mf.JSON()
}
