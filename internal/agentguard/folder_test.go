package agentguard

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// TestFolderFor pins the folder a session start names: the home directory
// at its start written as ~ by whole path elements, in any spelling the
// homes list, ignoring case and slash direction on Windows only, then the
// folder with its links resolved when the folder as named matched no home,
// and last the bound of localapi.CleanFolder, which keeps the end of a
// longer folder.
func TestFolderFor(t *testing.T) {
	a := func(n int) string { return strings.Repeat("a", n) }
	kim := []string{"/Users/kim"}
	win := []string{`C:\Users\kim`}
	cases := []struct {
		name, dir, real string
		homes           []string
		goos, want      string
	}{
		{"the home directory itself", "/Users/kim", "", kim, "darwin", "~"},
		{"a folder under the home directory", "/Users/kim/src/straza", "", kim, "darwin", "~/src/straza"},
		{"a home directory with a trailing slash", "/home/kim/src", "", []string{"/home/kim/"}, "linux", "~/src"},
		{"a sibling whose name starts with the home's", "/Users/kim2/x", "", kim, "darwin", "/Users/kim2/x"},
		{"a folder outside the home directory", "/opt/build", "", kim, "darwin", "/opt/build"},
		{"no home directory", "/Users/kim/src", "", nil, "darwin", "/Users/kim/src"},
		{"a home directory that is the root", "/usr/src", "", []string{"/"}, "linux", "/usr/src"},
		{"case counts outside Windows", "/users/kim/src", "", kim, "darwin", "/users/kim/src"},
		{"a symlinked home named by its physical path", "/data/home/kim/src", "", []string{"/home/kim", "/data/home/kim"}, "linux", "~/src"},
		{"a folder that resolves into the home through a link", "/work/app", "/Users/kim/src/app", kim, "darwin", "~/src/app"},
		{"a folder that resolves outside the home", "/work/app", "/opt/app", kim, "darwin", "/work/app"},
		{"the folder as named wins over its resolved form", "/Users/kim/proj", "/Volumes/data/proj", kim, "darwin", "~/proj"},
		{"exactly the bound", "/opt/" + a(localapi.FolderMax-5), "", kim, "darwin", "/opt/" + a(localapi.FolderMax-5)},
		{"the home is cut before the bound", "/Users/kim/" + a(localapi.FolderMax-2), "", kim, "darwin", "~/" + a(localapi.FolderMax-2)},
		{"over the bound with a rune on the cut", "/Users/kim/é" + a(localapi.FolderMax-1), "", kim, "darwin", a(localapi.FolderMax - 1)},
		{"a NUL character", "/Users/kim/\x00x", "", kim, "darwin", ""},
		{"bytes that are not UTF-8", "/Users/kim/\xff", "", kim, "darwin", ""},
		{"empty", "", "", kim, "darwin", ""},
		{"Windows ignores case", `c:\users\KIM\Src\App`, "", win, "windows", `~\Src\App`},
		{"Windows home itself in other case", `C:\USERS\KIM`, "", win, "windows", "~"},
		{"Windows keeps a forward slash", `C:\Users\kim/src`, "", win, "windows", "~/src"},
		{"Windows with forward slashes throughout", "C:/Users/kim/src", "", win, "windows", "~/src"},
		{"Windows home itself with forward slashes", "c:/users/KIM", "", win, "windows", "~"},
		{"Windows sibling", `C:\Users\kim2\x`, "", win, "windows", `C:\Users\kim2\x`},
		{"Windows home that is a drive root", `C:\src`, "", []string{`C:\`}, "windows", `C:\src`},
		{"slashes count apart outside Windows", `\Users\kim\src`, "", kim, "linux", `\Users\kim\src`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := folderFor(tc.dir, tc.real, tc.homes, tc.goos); got != tc.want {
				t.Fatalf("folderFor(%q, %q, %q, %s) = %q, want %q", tc.dir, tc.real, tc.homes, tc.goos, got, tc.want)
			}
		})
	}
}

// TestSessionFolderResolvesLinks pins that sessionFolder reads the home's
// resolved form and the folder's from the file system: a home reached
// through a link matches a folder named by its physical path, and a folder
// named through a link outside the home matches once it is resolved.
func TestSessionFolderResolvesLinks(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(real, "src", "app")
	if err := os.MkdirAll(app, 0o700); err != nil {
		t.Fatal(err)
	}
	links := t.TempDir()
	homeLink, appLink := filepath.Join(links, "home-link"), filepath.Join(links, "app-link")
	for target, link := range map[string]string{real: homeLink, app: appLink} {
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("this system makes no symlink: %v", err)
		}
	}
	want := "~" + string(filepath.Separator) + filepath.Join("src", "app")
	cases := []struct {
		name, home, dir string
	}{
		{"a home reached through a link, the folder by its physical path", homeLink, app},
		{"the real home, the folder through a link outside it", real, appLink},
		{"a home reached through a link, the folder through a link", homeLink, appLink},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", tc.home)
			t.Setenv("USERPROFILE", tc.home) // windows UserHomeDir
			if got := sessionFolder(tc.dir); got != want {
				t.Fatalf("sessionFolder(%q) with home %q = %q, want %q", tc.dir, tc.home, got, want)
			}
		})
	}
}

// TestSessionStartsSendTheFolder pins that the hook, `straza exec` and
// `straza mcp` each name the folder the session starts in when they ask the
// service for a session, with the home directory written as ~. HOME is a
// link to the real folder and each folder is named by its physical path, as
// a harness that reads getcwd names it, so the home's resolved form has to
// match.
func TestSessionStartsSendTheFolder(t *testing.T) {
	cases := []struct {
		name  string
		start func(t *testing.T, dir string) *fakeService
	}{
		{"the hook names the window's folder", func(t *testing.T, dir string) *fakeService {
			svc := newFakeService(t)
			payload, _ := json.Marshal(map[string]string{"hook_event_name": "SessionStart", "session_id": "s1", "cwd": dir})
			if _, _, err := runJournalHook(t, "claude-code", string(payload)); err != nil {
				t.Fatalf("session.start: %v", err)
			}
			return svc
		}},
		{"straza exec names the folder it runs in", func(t *testing.T, dir string) *fakeService {
			svc := newFakeService(t)
			t.Chdir(dir)
			var errOut bytes.Buffer
			if code := Exec(context.Background(), shellExit("0"), &errOut); code != 0 {
				t.Fatalf("exec = %d, %q", code, errOut.String())
			}
			return svc
		}},
		{"straza mcp names the folder it runs in", func(t *testing.T, dir string) *fakeService {
			svc := proxyService(t, newFakeGateway(t))
			t.Chdir(dir)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, st := mcp.NewInMemoryTransports()
			done := make(chan error, 1)
			go func() { done <- runMCPProxy(ctx, "claude-code", "", st) }()
			deadline := time.Now().Add(5 * time.Second)
			for len(svc.started()) == 0 && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			cancel()
			<-done
			return svc
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			real, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(t.TempDir(), "home-link")
			if err := os.Symlink(real, home); err != nil {
				t.Logf("this system makes no symlink (%v), so HOME is the real folder", err)
				home = real
			}
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home) // windows UserHomeDir
			dir := filepath.Join(real, "src", "app")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			starts := tc.start(t, dir).started()
			want := "~" + string(filepath.Separator) + filepath.Join("src", "app")
			if len(starts) != 1 || starts[0].Folder != want {
				t.Fatalf("session starts = %+v, want one naming the folder %q", starts, want)
			}
		})
	}
}
