package manager

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Watcher is the GitOps apps/ directory pipeline: it hands every
// new or changed *.yaml manifest to its propose callback and every file that
// left the directory to its gone callback, and strazad turns each into a
// draft a person publishes. It never installs, removes, writes, moves or
// deletes anything itself. It polls with a content fingerprint, portable
// across platforms and editors (atomic-rename saves, network mounts) where
// inotify-style watches are not, and one poll per second proposes a
// dropped file well within 3 s.
type Watcher struct {
	dir      string
	interval time.Duration
	log      *slog.Logger
	propose  func(ctx context.Context, f File) error
	gone     func(ctx context.Context, path, name string) error

	mu   sync.RWMutex
	seen map[string]watchedFile // absolute path → last state
	// dirRead is whether the last sweep read the directory.
	dirRead bool
}

// File is one manifest file of the watched directory as a sweep read it:
// its absolute path, the sha256 of its bytes, the bytes, the server name
// it declares, empty when it does not parse, and Was, the name the file's
// previous revision declared.
type File struct {
	Path, Hash string
	Raw        []byte
	Name, Was  string
}

// watchedFile is what the watcher keeps of one file: the revision it read
// last, the hash propose last took, and the name the file declares, which a
// revision that does not parse keeps, so that a typo never makes a server
// look undeclared. gone marks a file whose gone call failed, which is called
// again at the next sweep. unread marks a present file the last sweep could
// not read, or found empty, and empty a file found empty, which is read
// again once before it is proposed.
type watchedFile struct {
	file     File
	proposed string
	declared string
	gone     bool
	unread   bool
	empty    bool
}

// NewWatcher builds a watcher over dir. interval <= 0 defaults to 1 s.
func NewWatcher(dir string, interval time.Duration, log *slog.Logger) *Watcher {
	if interval <= 0 {
		interval = time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return &Watcher{dir: dir, interval: interval, log: log, seen: map[string]watchedFile{}}
}

// SetPropose sets what the watcher calls for a new or changed file, and at
// its first sweep for every file. A call that fails is made again at the
// next sweep. Call it before Run.
func (w *Watcher) SetPropose(fn func(ctx context.Context, f File) error) {
	w.propose = fn
}

// SetGone sets what the watcher calls when a file it saw is gone, with the
// server name its last parsed revision declared, empty when none parsed. A
// call that fails is made again at the next sweep. Call it before Run.
func (w *Watcher) SetGone(fn func(ctx context.Context, path, name string) error) {
	w.gone = fn
}

// Run polls until ctx is done.
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	w.Sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Sweep(ctx)
		}
	}
}

// SweepUntilRead sweeps now and at every tick until a sweep reads the
// directory, and answers false when ctx ends first. strazad runs its boot
// pass only after it, so a directory that is not mounted yet proposes no
// removal.
func (w *Watcher) SweepUntilRead(ctx context.Context) bool {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for !w.Sweep(ctx) {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
	}
	return true
}

// Sweep performs one pass over the directory (exported for strazad's first
// pass and for deterministic tests): every file whose bytes changed since
// propose last took it is proposed, and every file that left the directory
// is handed to gone. It answers whether it read the directory. A directory
// that cannot be read ends the sweep with no call, so an unmounted directory
// proposes no removal.
func (w *Watcher) Sweep(ctx context.Context) bool {
	paths, ok := w.manifests()
	w.mu.Lock()
	w.dirRead = ok
	w.mu.Unlock()
	if !ok {
		return false
	}
	present := map[string]bool{}
	for _, path := range paths {
		present[path] = true
		raw, err := os.ReadFile(path) // #nosec G304 -- the watched dir is operator config
		if err != nil {
			w.log.Warn("apps watcher: read", "file", path, "err", err)
			w.unread(path, false)
			continue
		}
		// An empty file is often a file being written, as cp leaves it for
		// a moment, so it is proposed only when a sweep later finds it
		// empty still.
		if len(raw) == 0 && !w.emptyBefore(path) {
			w.unread(path, true)
			continue
		}
		w.read(ctx, path, raw)
	}

	w.mu.RLock()
	var went []string
	for path := range w.seen {
		if !present[path] {
			went = append(went, path)
		}
	}
	w.mu.RUnlock()
	sort.Strings(went)
	for _, path := range went {
		w.mu.Lock()
		prev := w.seen[path]
		prev.gone = true
		w.seen[path] = prev
		w.mu.Unlock()
		if w.gone != nil {
			if err := w.gone(ctx, path, prev.declared); err != nil {
				w.log.Warn("apps watcher: the file is gone but its drafts could not be written, so the next sweep tries again",
					"file", path, "app", prev.declared, "err", err)
				continue
			}
		}
		w.mu.Lock()
		delete(w.seen, path)
		w.mu.Unlock()
	}
	return true
}

// unread marks the present file at path as one this sweep could not read,
// or found empty, keeping the name it declared before.
func (w *Watcher) unread(path string, empty bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e := w.seen[path]
	e.file.Path, e.unread, e.empty, e.gone = path, true, empty, false
	w.seen[path] = e
}

// emptyBefore reports whether the last sweep found the file at path empty.
func (w *Watcher) emptyBefore(path string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.seen[path].empty
}

// read hands the file at path with bytes raw to propose unless propose took
// these bytes already. A revision propose refused is proposed again, with
// the name it had before, at the next sweep.
func (w *Watcher) read(ctx context.Context, path string, raw []byte) {
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	w.mu.RLock()
	prev, had := w.seen[path]
	w.mu.RUnlock()
	if had && prev.proposed == hash {
		if prev.gone || prev.unread {
			w.store(path, watchedFile{file: prev.file, proposed: prev.proposed, declared: prev.declared, empty: len(raw) == 0})
		}
		return
	}
	f := prev.file
	if !had || f.Hash != hash {
		f = File{Path: path, Hash: hash, Raw: raw, Was: prev.declared}
		if mf, err := Parse(raw); err == nil {
			f.Name = mf.Metadata.Name
		}
	}
	next := watchedFile{file: f, proposed: prev.proposed, declared: cmp.Or(f.Name, prev.declared)}
	w.store(path, next)
	if w.propose != nil {
		if err := w.propose(ctx, f); err != nil {
			w.log.Warn("apps watcher: the file could not be proposed as a draft, so the next sweep tries again", "file", path, "err", err)
			return
		}
	}
	next.proposed = hash
	w.store(path, next)
}

func (w *Watcher) store(path string, e watchedFile) {
	w.mu.Lock()
	w.seen[path] = e
	w.mu.Unlock()
}

// FileFor answers the present file that declares the server name, the
// first by path when several do, and false when none does. A file whose
// latest revision does not parse still declares the name its last parsed
// revision did. Safe for concurrent use.
func (w *Watcher) FileFor(name string) (File, bool) {
	if name == "" {
		return File{}, false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	first := ""
	for path, e := range w.seen {
		if !e.gone && e.declared == name && (first == "" || path < first) {
			first = path
		}
	}
	if first == "" {
		return File{}, false
	}
	return w.seen[first].file, true
}

// Files answers every present file by path, each as its latest revision
// read, and whether the last sweep read the directory. A file that does not
// parse, that the last sweep could not read or that it found empty has no
// Name. Safe for concurrent use.
func (w *Watcher) Files() ([]File, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]File, 0, len(w.seen))
	for _, e := range w.seen {
		if e.gone {
			continue
		}
		f := e.file
		if e.unread {
			f.Name = ""
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, w.dirRead
}

// manifests lists the *.yaml and *.yml files of the watched directory. ok is
// false when the directory cannot be read.
func (w *Watcher) manifests() ([]string, bool) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		if !os.IsNotExist(err) {
			w.log.Warn("apps watcher: read dir", "dir", w.dir, "err", err)
		}
		return nil, false
	}
	var paths []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}
		paths = append(paths, filepath.Join(w.dir, name))
	}
	return paths, true
}
