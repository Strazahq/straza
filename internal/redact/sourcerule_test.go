package redact

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNoRawSecretIdentifiersInLogsOrErrors is the tripwire for the
// secrets-in-logs class: a failing HEC sink that echoes its query token on
// every retry, or a push delivery error that echoes the capability path. It
// scans every non-test .go file for lines that pass a KNOWN secret-bearing
// identifier into a formatting or logging call without going through
// redact.*.
//
// This is a line-based tripwire, not a proof: it catches the recurring
// shape (a named secret field reaching %s/%v in a log or error), and a new
// secret identifier must be added to deny below when introduced. A line
// that is genuinely safe can carry a trailing "// secretok: <why>" marker:
// the marker forces the reason into the diff for review.
func TestNoRawSecretIdentifiersInLogsOrErrors(t *testing.T) {
	root := moduleRoot(t)
	// Identifiers whose VALUE is (or can embed) a credential. Method calls
	// and struct construction are fine; only format/log lines are flagged.
	deny := []string{
		".TokenOrEndpoint", "Events.URL", ".BotToken",
		".SigningSecret", ".ClientSecret", ".SecretKey", ".MetricsToken",
		".DSN", "vapidPrivateKey", ".AccessToken", ".RefreshToken",
	}
	sinks := []string{
		"fmt.Errorf(", "errors.New(", "fmt.Sprintf(",
		".Warn(", ".Info(", ".Error(", ".Debug(", "Fprintf(", "Printf(",
	}
	var violations []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Stray tool workdirs (overlay leftovers under test/harness-matrix
			// come back mode-000) must not kill the scan; source dirs we CAN
			// read are still fully swept.
			if os.IsPermission(err) {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".claude" || name == "vendor" ||
				name == "node_modules" || name == "build" ||
				strings.HasPrefix(name, ".work") || name == ".tools" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path) // #nosec G304 -- repo source scan
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "redact.") || strings.Contains(line, "// secretok:") {
				continue
			}
			hasSink := false
			for _, s := range sinks {
				if strings.Contains(line, s) {
					hasSink = true
					break
				}
			}
			if !hasSink {
				continue
			}
			for _, dny := range deny {
				if strings.Contains(line, dny) {
					rel, _ := filepath.Rel(root, path)
					violations = append(violations, rel+":"+itoa(i+1)+" formats "+dny)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("secret-bearing identifiers reach a log/error format without redact.*. "+
			"Redact (redact.URL / redact.Host / redact.SanitizeURLError) or justify with "+
			"a trailing `// secretok: <why>`:\n  %s", strings.Join(violations, "\n  "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", file)
		}
		dir = parent
	}
}
