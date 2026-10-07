package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// fileText is a valid remote manifest of the server name at url, as an
// operator writes it into the apps directory.
func fileText(name, url string) string {
	return "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata: {name: " + name + "}\n" +
		"server: {name: straza.test/" + name + ", version: \"1.0.0\"}\n" +
		"straza:\n  runtime:\n    kind: remote\n    remote: {url: " + url + "}\n"
}

// goneCall is one call of the gone callback.
type goneCall struct{ path, name string }

// recorder stands in for the server's callbacks: it keeps every call, and
// answers the next call of each with the error set for it once.
type recorder struct {
	mu          sync.Mutex
	proposed    []File
	gone        []goneCall
	failPropose error
	failGone    error
}

func (r *recorder) propose(_ context.Context, f File) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.proposed = append(r.proposed, f)
	err := r.failPropose
	r.failPropose = nil
	return err
}

func (r *recorder) goneFile(_ context.Context, path, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gone = append(r.gone, goneCall{path, name})
	err := r.failGone
	r.failGone = nil
	return err
}

// take answers the calls since the last take and forgets them.
func (r *recorder) take() ([]File, []goneCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, g := r.proposed, r.gone
	r.proposed, r.gone = nil, nil
	return p, g
}

// watch is a watcher over dir with r as its callbacks.
func (r *recorder) watch(dir string) *Watcher {
	w := NewWatcher(dir, time.Second, nil)
	w.SetPropose(r.propose)
	w.SetGone(r.goneFile)
	return w
}

// fileDir is an apps directory with its writers: put writes a file, drop
// removes one, and path answers a file's absolute path.
type fileDir struct {
	t   *testing.T
	dir string
}

func newFileDir(t *testing.T) fileDir {
	t.Helper()
	dir, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return fileDir{t, dir}
}

func (d fileDir) path(name string) string { return filepath.Join(d.dir, name) }

func (d fileDir) put(name, text string) {
	d.t.Helper()
	if err := os.WriteFile(d.path(name), []byte(text), 0o600); err != nil {
		d.t.Fatal(err)
	}
}

func (d fileDir) drop(name string) {
	d.t.Helper()
	if err := os.Remove(d.path(name)); err != nil {
		d.t.Fatal(err)
	}
}

// proposal is what a test expects of one propose call.
type proposal struct{ file, name, was, text string }

func hashOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// TestWatcherProposes pins what a sweep hands to propose: a new or changed
// file once, with its absolute path, the sha256 of its bytes, the bytes,
// the server name it declares, empty when it does not parse, and the name
// its last parsed revision declared. An unchanged file, an empty one, a file
// of another type and a subdirectory are never proposed, and an unreadable
// directory ends the sweep with no call at all.
func TestWatcherProposes(t *testing.T) {
	const bad = "kind: Nope\n"
	one, two := fileText("github", "https://one.example/mcp"), fileText("github", "https://two.example/mcp")
	renamed := fileText("gitlab", "https://two.example/mcp")
	cases := []struct {
		name  string
		steps []func(d fileDir)
		want  [][]proposal
	}{
		{"a new file", []func(fileDir){func(d fileDir) { d.put("a.yaml", one) }},
			[][]proposal{{{"a.yaml", "github", "", one}}}},
		{"an unchanged file is proposed once", []func(fileDir){func(d fileDir) { d.put("a.yaml", one) }, func(fileDir) {}},
			[][]proposal{{{"a.yaml", "github", "", one}}, nil}},
		{"a changed file", []func(fileDir){func(d fileDir) { d.put("a.yaml", one) }, func(d fileDir) { d.put("a.yaml", two) }},
			[][]proposal{{{"a.yaml", "github", "", one}}, {{"a.yaml", "github", "github", two}}}},
		{"a renamed metadata.name", []func(fileDir){func(d fileDir) { d.put("a.yaml", one) }, func(d fileDir) { d.put("a.yaml", renamed) }},
			[][]proposal{{{"a.yaml", "github", "", one}}, {{"a.yaml", "gitlab", "github", renamed}}}},
		{"a revision that does not parse keeps the name before it", []func(fileDir){
			func(d fileDir) { d.put("a.yaml", one) }, func(d fileDir) { d.put("a.yaml", bad) }, func(d fileDir) { d.put("a.yaml", renamed) }},
			[][]proposal{{{"a.yaml", "github", "", one}}, {{"a.yaml", "", "github", bad}}, {{"a.yaml", "gitlab", "github", renamed}}}},
		{"only yaml and yml files are read", []func(fileDir){func(d fileDir) {
			d.put("b.yml", one)
			d.put("notes.json", one)
			if err := os.Mkdir(d.path("sub"), 0o750); err != nil {
				t.Fatal(err)
			}
			d.put(filepath.Join("sub", "c.yaml"), one)
		}}, [][]proposal{{{"b.yml", "github", "", one}}}},
		{"an empty file is read again until it holds something", []func(fileDir){
			func(d fileDir) { d.put("a.yaml", "") }, func(d fileDir) { d.put("a.yaml", one) }},
			[][]proposal{nil, {{"a.yaml", "github", "", one}}}},
		{"an unreadable directory calls nothing", []func(fileDir){func(d fileDir) {
			d.put("a.yaml", one)
			if err := os.RemoveAll(d.dir); err != nil {
				t.Fatal(err)
			}
		}}, [][]proposal{nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newFileDir(t)
			r := &recorder{}
			w := r.watch(d.dir)
			for i, step := range tc.steps {
				step(d)
				w.Sweep(context.Background())
				got, gone := r.take()
				if len(gone) != 0 {
					t.Errorf("sweep %d called gone %v", i+1, gone)
				}
				var want []File
				for _, p := range tc.want[i] {
					want = append(want, File{Path: d.path(p.file), Hash: hashOf(p.text), Raw: []byte(p.text), Name: p.name, Was: p.was})
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("sweep %d proposed %+v, want %+v", i+1, got, want)
				}
			}
		})
	}
}

// TestWatcherCallsGone pins the gone callback: a file that left the
// directory is handed over once with the name its last parsed revision
// declared, empty when no revision parsed.
func TestWatcherCallsGone(t *testing.T) {
	one := fileText("github", "https://one.example/mcp")
	cases := []struct {
		name      string
		revisions []string
		want      string
	}{
		{"a file", []string{one}, "github"},
		{"a file that never parsed", []string{"kind: Nope\n"}, ""},
		{"a file whose last revision does not parse", []string{one, "kind: Nope\n"}, "github"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newFileDir(t)
			r := &recorder{}
			w := r.watch(d.dir)
			for _, text := range tc.revisions {
				d.put("a.yaml", text)
				w.Sweep(context.Background())
			}
			d.drop("a.yaml")
			w.Sweep(context.Background())
			w.Sweep(context.Background())
			_, gone := r.take()
			if want := []goneCall{{d.path("a.yaml"), tc.want}}; !reflect.DeepEqual(gone, want) {
				t.Errorf("gone calls = %v, want %v", gone, want)
			}
		})
	}
}

// TestWatcherTriesAgainAfterAFailedCallback pins that a propose or a gone
// that fails, a store outage for one, is called again at the next sweep
// with the same file, and not again once it succeeded.
func TestWatcherTriesAgainAfterAFailedCallback(t *testing.T) {
	d := newFileDir(t)
	r := &recorder{failPropose: errors.New("the store is down")}
	w := r.watch(d.dir)
	ctx := context.Background()
	d.put("a.yaml", fileText("github", "https://one.example/mcp"))
	d.put("b.yaml", fileText("jira", "https://one.example/mcp"))
	w.Sweep(ctx)
	first, _ := r.take()
	d.put("b.yaml", fileText("jira2", "https://one.example/mcp"))
	w.Sweep(ctx)
	again, _ := r.take()
	if len(first) != 2 || len(again) != 2 || !reflect.DeepEqual(again[0], first[0]) {
		t.Fatalf("after a failed propose the next sweep proposed %+v, want %+v again beside b.yaml", again, first[0])
	}
	if again[1].Was != "jira" {
		t.Errorf("b.yaml's next revision names %q as its last name, want jira", again[1].Was)
	}
	w.Sweep(ctx)
	if p, _ := r.take(); len(p) != 0 {
		t.Errorf("a sweep after the proposals succeeded proposed %+v", p)
	}

	r.failGone = errors.New("the store is down")
	d.drop("a.yaml")
	w.Sweep(ctx)
	if _, ok := w.FileFor("github"); ok {
		t.Error("FileFor answers a file whose gone call failed")
	}
	w.Sweep(ctx)
	w.Sweep(ctx)
	if _, gone := r.take(); len(gone) != 2 || gone[0] != gone[1] {
		t.Errorf("gone calls = %v, want the failed call and one more", gone)
	}
}

// TestWatcherFileFor pins FileFor and Files: the first present file by path
// that declares the name, a revision that does not parse keeping its name,
// and nothing once the file is gone. Files lists every present file with
// the name of its current revision.
func TestWatcherFileFor(t *testing.T) {
	d := newFileDir(t)
	w := (&recorder{}).watch(d.dir)
	ctx := context.Background()
	one := fileText("github", "https://one.example/mcp")
	d.put("b.yaml", one)
	d.put("a.yaml", one)
	w.Sweep(ctx)
	if f, ok := w.FileFor("github"); !ok || f.Path != d.path("a.yaml") || string(f.Raw) != one {
		t.Errorf("FileFor(github) = %+v %v, want a.yaml", f, ok)
	}
	d.put("a.yaml", "kind: Nope\n")
	w.Sweep(ctx)
	if f, ok := w.FileFor("github"); !ok || f.Path != d.path("a.yaml") || f.Name != "" {
		t.Errorf("FileFor(github) over a revision that does not parse = %+v %v, want a.yaml with no name", f, ok)
	}
	files, _ := w.Files()
	if len(files) != 2 || files[0].Path != d.path("a.yaml") || files[0].Name != "" || files[1].Name != "github" {
		t.Errorf("Files = %+v, want a.yaml with no name, then b.yaml naming github", files)
	}
	d.drop("a.yaml")
	w.Sweep(ctx)
	if f, ok := w.FileFor("github"); !ok || f.Path != d.path("b.yaml") {
		t.Errorf("FileFor(github) = %+v %v, want b.yaml", f, ok)
	}
	d.drop("b.yaml")
	w.Sweep(ctx)
	if f, ok := w.FileFor("github"); ok {
		t.Errorf("FileFor(github) with no file = %+v, want none", f)
	}
	if _, ok := w.FileFor(""); ok {
		t.Error("FileFor of an empty name answers a file")
	}
}

// TestWatcherFileForWhileSweeping pins that FileFor and Files are safe while
// a sweep runs, for the race detector.
func TestWatcherFileForWhileSweeping(t *testing.T) {
	d := newFileDir(t)
	w := (&recorder{}).watch(d.dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			w.FileFor("github")
			w.Files()
		}
	}()
	for i := range 20 {
		d.put("a.yaml", fileText("github", "https://one.example/mcp/"+string(rune('a'+i))))
		w.Sweep(ctx)
	}
	cancel()
	<-done
}

// TestWatcherAnswersWhetherItReadTheDirectory pins what a boot pass reads
// before it proposes a removal: Sweep and Files answer whether the sweep
// read the directory, and a present file that could not be read, or that
// is empty, is listed with no name, so it counts as a file that does not
// read, while FileFor keeps the name its last read revision declared.
func TestWatcherAnswersWhetherItReadTheDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	ctx := context.Background()
	names := func(files []File) map[string]string {
		out := map[string]string{}
		for _, f := range files {
			out[filepath.Base(f.Path)] = f.Name
		}
		return out
	}
	d := newFileDir(t)
	w := (&recorder{}).watch(d.dir)
	if files, read := w.Files(); read || len(files) != 0 {
		t.Errorf("Files before any sweep = %v %v, want nothing read", files, read)
	}
	d.put("a.yaml", fileText("github", "https://one.example/mcp"))
	d.put("b.yaml", fileText("jira", "https://one.example/mcp"))
	d.put("empty.yaml", "")
	if err := os.Chmod(d.path("b.yaml"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(d.path("b.yaml"), 0o600) })
	if !w.Sweep(ctx) {
		t.Fatal("a sweep of a readable directory answered false")
	}
	files, read := w.Files()
	if want := map[string]string{"a.yaml": "github", "b.yaml": "", "empty.yaml": ""}; !read || !reflect.DeepEqual(names(files), want) {
		t.Errorf("Files = %v %v, want %v and read", names(files), read, want)
	}
	if err := os.Chmod(d.path("b.yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.Sweep(ctx)
	if err := os.Chmod(d.path("b.yaml"), 0); err != nil {
		t.Fatal(err)
	}
	w.Sweep(ctx)
	if f, ok := w.FileFor("jira"); !ok || f.Path != d.path("b.yaml") {
		t.Errorf("FileFor(jira) of a file that can no longer be read = %+v %v, want b.yaml", f, ok)
	}
	if files, _ := w.Files(); names(files)["b.yaml"] != "" {
		t.Errorf("Files names %q for a file that can no longer be read, want no name", names(files)["b.yaml"])
	}
	if err := os.Chmod(d.dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(d.dir, 0o750) })
	if w.Sweep(ctx) {
		t.Error("a sweep of a directory that cannot be read answered true")
	}
	if _, read := w.Files(); read {
		t.Error("Files says the last sweep read a directory it could not read")
	}
}

// TestWatcherProposesAFileStillEmptyASweepLater pins that an empty file is
// read again once, a sweep later, and proposed with no name when it is
// still empty, since an empty file does not parse, and proposed once.
func TestWatcherProposesAFileStillEmptyASweepLater(t *testing.T) {
	d := newFileDir(t)
	r := &recorder{}
	w := r.watch(d.dir)
	ctx := context.Background()
	d.put("a.yaml", fileText("github", "https://one.example/mcp"))
	w.Sweep(ctx)
	r.take()
	d.put("a.yaml", "")
	w.Sweep(ctx)
	if p, g := r.take(); len(p) != 0 || len(g) != 0 {
		t.Fatalf("the first sweep of an empty file called %+v and %v, want nothing", p, g)
	}
	w.Sweep(ctx)
	p, _ := r.take()
	if want := []File{{Path: d.path("a.yaml"), Hash: hashOf(""), Raw: []byte{}, Was: "github"}}; !reflect.DeepEqual(p, want) {
		t.Errorf("the second sweep proposed %+v, want %+v", p, want)
	}
	for range 3 {
		w.Sweep(ctx)
	}
	if p, _ := r.take(); len(p) != 0 {
		t.Errorf("later sweeps proposed %+v, want nothing", p)
	}
}

// TestWatcherSweepsUntilItReadsTheDirectory pins SweepUntilRead: it sweeps
// at every tick until a sweep reads the directory, and gives up when its
// context ends.
func TestWatcherSweepsUntilItReadsTheDirectory(t *testing.T) {
	d := newFileDir(t)
	missing := filepath.Join(d.dir, "apps")
	r := &recorder{}
	w := NewWatcher(missing, 20*time.Millisecond, nil)
	w.SetPropose(r.propose)
	go func() {
		time.Sleep(100 * time.Millisecond)
		if err := os.Mkdir(missing, 0o750); err == nil {
			_ = os.WriteFile(filepath.Join(missing, "a.yaml"), []byte(fileText("github", "https://one.example/mcp")), 0o600)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !w.SweepUntilRead(ctx) {
		t.Fatal("SweepUntilRead gave up before the directory appeared")
	}
	if _, read := w.Files(); !read {
		t.Error("Files says the directory was not read")
	}

	gone := NewWatcher(filepath.Join(d.dir, "never"), 20*time.Millisecond, nil)
	short, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	if gone.SweepUntilRead(short) {
		t.Error("SweepUntilRead answered true for a directory that never appeared")
	}
}

// TestWatcherNeverWritesTheDirectory pins that the watcher only reads: the
// names, bytes and modification times of the directory are the same after
// sweeps over new, changed, invalid and removed files, whatever the
// callbacks answer.
func TestWatcherNeverWritesTheDirectory(t *testing.T) {
	d := newFileDir(t)
	r := &recorder{failPropose: errors.New("refused")}
	w := r.watch(d.dir)
	ctx := context.Background()
	state := func() map[string]string {
		entries, err := os.ReadDir(d.dir)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(d.path(e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out[e.Name()] = info.ModTime().String() + " " + string(raw)
		}
		return out
	}
	d.put("a.yaml", fileText("github", "https://one.example/mcp"))
	d.put("bad.yaml", "kind: Nope\n")
	d.put("gone.yaml", fileText("jira", "https://one.example/mcp"))
	w.Sweep(ctx)
	d.put("a.yaml", fileText("github", "https://two.example/mcp"))
	d.drop("gone.yaml")
	before := state()
	for range 3 {
		w.Sweep(ctx)
	}
	if after := state(); !reflect.DeepEqual(after, before) {
		t.Errorf("the directory changed under the watcher: before %v, after %v", before, after)
	}
}
