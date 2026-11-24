package agentguard

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/strazahq/straza/internal/agentguard/localapi"
)

// sessionFolder is the folder a session start names for dir, the folder
// the session starts in: the person's home directory at its start written
// as ~, then bound by localapi.CleanFolder. The hook, `straza exec` and
// `straza mcp` all call it. It runs in the thin client, as the person, so
// the home directory it reads is the person's. It reads the home as the
// OS names it and with its links resolved, and dir with its links
// resolved, because a harness may name its folder by the physical path.
// When the home cannot be read, dir goes as it is.
func sessionFolder(dir string) string {
	var homes []string
	if home, err := os.UserHomeDir(); err == nil {
		homes = append(homes, home)
		if real, err := filepath.EvalSymlinks(home); err == nil {
			homes = append(homes, real)
		}
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		real = ""
	}
	return folderFor(dir, real, homes, runtime.GOOS)
}

// folderFor is sessionFolder with real, dir with its links resolved or
// empty, the spellings of the home directory and the OS given. dir is
// matched against every home first, and real only when dir matched none.
// A dir that holds a NUL character or is not valid UTF-8 becomes empty
// before anything else, so the home directory never stands in for bytes
// the server would refuse.
func folderFor(dir, real string, homes []string, goos string) string {
	if strings.ContainsRune(dir, 0) || !utf8.ValidString(dir) {
		return ""
	}
	for _, d := range []string{dir, real} {
		for _, home := range homes {
			if rest, ok := underHome(d, home, goos); ok {
				return localapi.CleanFolder("~" + rest)
			}
		}
	}
	return localapi.CleanFolder(dir)
}

// underHome reports whether dir is home or lies under it by whole path
// elements, and returns the rest of dir after home. On Windows the match
// ignores case and takes a slash and a backslash as the same separator. A
// home that is empty, or a root such as / or C:\, matches nothing.
func underHome(dir, home, goos string) (string, bool) {
	seps := "/"
	if goos == "windows" {
		seps = `\/`
	}
	home = strings.TrimRight(home, seps)
	if dir == "" || home == "" || (goos == "windows" && len(home) == 2 && home[1] == ':') || len(dir) < len(home) {
		return "", false
	}
	head, rest := dir[:len(home)], dir[len(home):]
	same := head == home
	if goos == "windows" {
		same = strings.EqualFold(strings.ReplaceAll(head, "/", `\`), strings.ReplaceAll(home, "/", `\`))
	}
	if !same || (rest != "" && !strings.ContainsRune(seps, rune(rest[0]))) {
		return "", false
	}
	return rest, true
}
