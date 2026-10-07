package drafts

import (
	"reflect"
	"testing"
)

// TestCheckRefusesCommandServers: a server that refuses command servers
// refuses a draft that puts one, with the sentence and the fix the manager's
// CheckRuntime answers, and passes a remote server. A server that runs
// command servers passes the same draft.
func TestCheckRefusesCommandServers(t *testing.T) {
	t.Parallel()
	command := App{Runtime: "command", Exec: "/usr/local/bin/files-mcp", Credential: CredentialNone}
	remote := App{Runtime: "remote", URL: "https://files.example.com", Credential: CredentialNone}
	refused := []Finding{{Code: "app.runtime", Class: ClassRefused, Object: "App/files",
		Sentence: "files runs as a command, which this server refuses under the enterprise profile, because the process would run inside Straza with access to its keys and database.",
		Fix:      "Run the server as its own service or pod and add it as a remote server over HTTP."}}
	cases := []struct {
		name   string
		refuse bool
		app    App
		want   []Finding
	}{
		{"enterprise refuses a command server", true, command, refused},
		{"enterprise passes a remote server", true, remote, []Finding{}},
		{"standalone passes a command server", false, command, []Finding{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := checkWorld()
			w.RefuseCommand = tc.refuse
			d := stamped(w, appPut("files"))
			v := Check(w, d, CheckInput{Apps: map[string]App{"files": tc.app}, Now: checkNow})
			if !reflect.DeepEqual(v.Refused, tc.want) {
				t.Errorf("Refused\n got %+v\nwant %+v", v.Refused, tc.want)
			}
		})
	}
}
